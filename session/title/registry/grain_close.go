package titleregistry

import (
	"context"
	"errors"

	agentactor "github.com/gorenx/goren/agent/actor"
	"github.com/gorenx/goren/session"
	titleactor "github.com/gorenx/goren/session/title/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
)

type closed struct {
	entry             *instance
	subscriberStopped bool
	drained           bool
	err               error
}

func (owner *Grain) stop(gc *goakt.GrainContext, input agentactor.StopComponent) {
	if input.AgentID == "" || input.CreationID == "" {
		gc.Response(agentactor.ComponentStopped{
			Err: errors.New("title registry: creation is required"),
		})
		return
	}
	entry := owner.instances[input.AgentID]
	if entry == nil {
		if owner.instances == nil {
			owner.instances = make(map[session.SessionID]*instance)
		}
		owner.instances[input.AgentID] = &instance{
			input: agentactor.StartComponent{
				AgentID:    input.AgentID,
				CreationID: input.CreationID,
			},
			phase: stopped,
		}
		gc.Response(agentactor.ComponentStopped{})
		return
	}
	if entry.input.CreationID != input.CreationID {
		gc.Response(agentactor.ComponentStopped{
			Err: errors.New("title registry: creation is not current"),
		})
		return
	}
	if entry.phase == stopped {
		gc.Response(agentactor.ComponentStopped{})
		return
	}
	reply := gc.DeferResponse()
	if reply == nil {
		gc.Response(agentactor.ComponentStopped{
			Err: errors.New("title registry: close requires a request"),
		})
		return
	}
	entry.stoppers = append(entry.stoppers, reply)
	if entry.phase == starting {
		if entry.cancel != nil {
			entry.cancel()
		}
		return
	}
	if entry.cancel == nil {
		owner.runStop(gc, entry)
	}
}

func (owner *Grain) runStop(gc *goakt.GrainContext, entry *instance) {
	entry.phase = stopping
	if entry.endpoint == nil && entry.subscriber == nil {
		entry.phase = stopped
		entry.replyStop(nil)
		return
	}
	// Capture framework handles and completion facts; the task never reads owner state.
	endpoint, subscriber, drained := entry.endpoint, entry.subscriber, entry.drained
	creationID := entry.input.CreationID
	timeout := owner.settings.Timeout
	system := gc.ActorSystem()
	ctx, cancel := context.WithCancel(context.WithoutCancel(gc.Context()))
	entry.cancel = cancel
	if err := gc.PipeToSelf(func() (any, error) {
		result := closed{
			entry:   entry,
			drained: drained,
		}
		if subscriber != nil && subscriber.IsRunning() {
			result.err = system.Kill(ctx, subscriber.Name())
		}
		result.subscriberStopped = result.err == nil
		if result.err == nil && endpoint != nil && !drained {
			payload, err := system.AskGrain(ctx, endpoint, titleactor.Close{
				CreationID: creationID,
			}, timeout)
			if err == nil {
				completed, ok := payload.(titleactor.Completion)
				if !ok {
					err = errors.New("title registry: invalid close reply")
				} else {
					err = completed.Err
				}
			}
			result.err = err
			result.drained = err == nil
		}
		if result.err == nil && endpoint != nil {
			result.err = system.TellGrain(ctx, endpoint, new(goakt.PoisonPill))
		}
		return result, nil
	}); err != nil {
		cancel()
		entry.cancel = nil
		entry.replyStop(err)
	}
}

func (owner *Grain) finishStop(_ *goakt.GrainContext, result closed) {
	entry := result.entry
	if owner.instances[entry.input.AgentID] != entry || entry.phase != stopping {
		return
	}
	entry.cancel()
	entry.cancel = nil
	if result.subscriberStopped {
		entry.subscriber = nil
	}
	entry.drained = result.drained
	if result.err == nil {
		entry.phase = stopped
		entry.endpoint = nil
	}
	entry.replyStop(result.err)
}
