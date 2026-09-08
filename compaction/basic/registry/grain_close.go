package compactionregistry

import (
	"context"
	"errors"
	"github.com/gorenx/goren/session"

	agentactor "github.com/gorenx/goren/agent/actor"
	compactionactor "github.com/gorenx/goren/compaction/basic/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

func (owner *Grain) stop(gc *goakt.GrainContext, input agentactor.StopComponent) {
	if input.AgentID == "" || input.CreationID == "" {
		gc.Response(agentactor.ComponentStopped{
			Err: errors.New("compaction registry: creation is required"),
		})
		return
	}
	entry := owner.instances[input.AgentID]
	if entry == nil {
		// Record a tombstone so a late startup cannot acquire resources after close.
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
			Err: errors.New("compaction registry: creation is not current"),
		})
		return
	}
	if entry.phase == stopped {
		gc.Response(agentactor.ComponentStopped{})
		return
	}
	if err := gc.EnableReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))); err != nil {
		gc.Response(agentactor.ComponentStopped{
			Err: err,
		})
		return
	}
	reply := gc.DeferResponse()
	if reply == nil {
		gc.Response(agentactor.ComponentStopped{
			Err: errors.New("compaction registry: shutdown requires a request"),
		})
		return
	}
	entry.stoppers = append(entry.stoppers, reply)
	if entry.cancel == nil {
		owner.runStop(gc, entry)
	}
}

type closed struct {
	entry   *instance
	drained bool
	err     error
}

func (owner *Grain) runStop(gc *goakt.GrainContext, entry *instance) {
	entry.phase = closing
	if entry.runtime == nil {
		entry.phase = stopped
		entry.replyStop(nil)
		return
	}
	ctx, cancel := context.WithCancel(context.WithoutCancel(gc.Context()))
	entry.cancel = cancel
	completed := make(chan error, 1)
	system := gc.ActorSystem()
	target := entry.runtime
	if err := gc.PipeToSelf(func() (any, error) {
		var failure error
		select {
		case failure = <-completed:
		case <-ctx.Done():
			failure = context.Cause(ctx)
		}
		result := closed{
			entry:   entry,
			drained: failure == nil,
			err:     failure,
		}
		if failure == nil {
			result.err = system.TellGrain(ctx, target, new(goakt.PoisonPill))
		}
		return result, nil
	}); err != nil {
		cancel()
		entry.cancel = nil
		entry.replyStop(err)
		return
	}
	if entry.drained {
		completed <- nil
		return
	}
	gc.RequestGrain(target, compactionactor.Close{}, goakt.WithRequestTimeout(0)).Then(func(payload any, failure error) {
		if failure == nil {
			result, ok := payload.(compactionactor.Completion)
			if !ok {
				failure = errors.New("compaction registry: invalid close reply")
			} else {
				failure = result.Err
			}
		}
		completed <- failure
	})
}

func (owner *Grain) finishStop(_ *goakt.GrainContext, input closed) {
	entry := input.entry
	if owner.instances[entry.input.AgentID] != entry || entry.phase != closing {
		return
	}
	entry.cancel()
	entry.cancel = nil
	entry.drained = entry.drained || input.drained
	if input.err == nil {
		entry.phase = stopped
		entry.runtime = nil
	}
	entry.replyStop(input.err)
}

func (entry *instance) replyStop(failure error) {
	for _, reply := range entry.stoppers {
		reply.Response(agentactor.ComponentStopped{
			Err: failure,
		})
	}
	entry.stoppers = nil
}
