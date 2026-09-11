package titleregistry

import (
	"context"
	"errors"

	agentactor "github.com/gorenx/goren/agent/actor"
	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	titleactor "github.com/gorenx/goren/session/title/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

type initialized struct {
	entry      *instance
	endpoint   *goakt.GrainIdentity
	subscriber *goakt.PID
	err        error
}

func (owner *Grain) start(gc *goakt.GrainContext, input agentactor.StartComponent) {
	if owner.settings == nil || input.AgentID == "" || input.CreationID == "" || input.Session == nil ||
		input.Session.Name() != sessionactor.RuntimeName(string(input.AgentID)) {
		gc.Response(agentactor.ComponentStarted{
			Err: errors.New("title registry: configured owner and matching Session required"),
		})
		return
	}
	entry := owner.instances[input.AgentID]
	if entry != nil && (entry.phase != stopped || entry.input.CreationID == input.CreationID) {
		if entry.input.CreationID != input.CreationID || entry.phase == stopped || entry.phase == stopping ||
			len(entry.stoppers) != 0 || !entry.input.Session.Equal(input.Session) {
			gc.Response(agentactor.ComponentStarted{
				Err: errors.New("title registry: creation is not available"),
			})
			return
		}
		if entry.phase != starting {
			result := agentactor.ComponentStarted{
				Err: entry.failure,
			}
			if result.Err == nil {
				result.Runtime = entry.endpoint
			}
			gc.Response(result)
			return
		}
	}
	reply := gc.DeferResponse()
	if reply == nil {
		gc.Response(agentactor.ComponentStarted{
			Err: errors.New("title registry: startup requires a request"),
		})
		return
	}
	if entry != nil && entry.phase == starting {
		entry.starters = append(entry.starters, reply)
		return
	}
	entry = &instance{
		input:    input,
		phase:    starting,
		starters: []*goakt.GrainReply{reply},
	}
	if owner.instances == nil {
		owner.instances = make(map[session.SessionID]*instance)
	}
	owner.instances[input.AgentID] = entry
	settings := *owner.settings
	ctx, cancel := context.WithCancel(context.WithoutCancel(gc.Context()))
	entry.cancel = cancel
	system := gc.ActorSystem()
	if err := gc.PipeToSelf(func() (any, error) {
		result := initialized{
			entry: entry,
		}
		endpoint, err := goakt.GrainOf[*titleactor.Grain](ctx, system, titleactor.RuntimeName(string(input.AgentID)),
			goakt.WithLongLivedGrain(), goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
		result.endpoint = endpoint
		if err == nil {
			var payload any
			payload, err = system.AskGrain(ctx, input.Session, sessionactor.Read{
				CreationID: input.CreationID,
			}, settings.Timeout)
			if err == nil {
				view, ok := payload.(sessionactor.View)
				if !ok {
					err = errors.New("title registry: invalid Session reply")
				} else if view.Err != nil {
					err = view.Err
				} else {
					payload, err = system.AskGrain(ctx, endpoint, titleactor.Initialize{
						CreationID: input.CreationID,
						Session:    input.Session,
						Header:     view.Header,
						Config:     settings.Config,
						Provider:   settings.Provider,
					}, settings.Timeout)
					if err == nil {
						completed, ok := payload.(titleactor.Completion)
						if !ok {
							err = errors.New("title registry: invalid initialization reply")
						} else {
							err = completed.Err
						}
					}
				}
			}
		}
		if err == nil {
			listener, failure := titleactor.NewSubscriber(endpoint, input.CreationID, input.AgentID)
			err = failure
			if err == nil {
				result.subscriber, err = system.Spawn(ctx, string(input.AgentID)+".title-events", listener,
					goakt.WithReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
				if err == nil {
					waiting, stop := context.WithTimeout(ctx, settings.Timeout)
					select {
					case <-listener.Ready():
					case <-waiting.Done():
						err = waiting.Err()
					}
					stop()
				}
			}
		}
		result.err = err
		return result, nil
	}); err != nil {
		cancel()
		entry.cancel = nil
		entry.phase = failed
		entry.failure = err
		entry.replyStart(err)
	}
}

func (owner *Grain) finishStart(gc *goakt.GrainContext, result initialized) {
	entry := result.entry
	if owner.instances[entry.input.AgentID] != entry || entry.phase != starting {
		return
	}
	entry.cancel()
	entry.cancel = nil
	entry.endpoint = result.endpoint
	entry.subscriber = result.subscriber
	entry.failure = result.err
	entry.phase = available
	if result.err != nil {
		entry.phase = failed
	}
	entry.replyStart(result.err)
	if len(entry.stoppers) != 0 {
		owner.runStop(gc, entry)
	}
}
