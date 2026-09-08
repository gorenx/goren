package registry

import (
	"errors"

	agentactor "github.com/gorenx/goren/agent/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

func (owner *Grain) resolve(gc *goakt.GrainContext, input Resolve) {
	if input.WaitReady {
		entry, found := owner.creations[input.AgentID]
		if found &&
			!entry.closed &&
			entry.activation != nil &&
			entry.activation.initializing {
			if failure := owner.directory.CheckAdmission(input.AgentID); failure != nil {
				gc.Response(Ready{
					Err: failure,
				})
				return
			}
			reply := gc.DeferResponse()
			if reply == nil {
				gc.Response(Ready{
					Err: errors.New("agent registry: resolution requires a request"),
				})
				return
			}
			entry.activation.replies = append(entry.activation.replies, reply)
			return
		}
	}
	result := owner.ready(input.AgentID)
	if result.Err != nil {
		gc.Response(result)
		return
	}
	reply := gc.DeferResponse()
	if reply == nil {
		gc.Response(Ready{
			Err: errors.New("agent registry: resolution requires a request"),
		})
		return
	}
	original := owner.creations[input.AgentID]
	gc.RequestGrain(
		original.executor,
		agentactor.Verify{
			CreationID: original.creationID,
			InstanceID: original.instanceID,
		},
		goakt.WithRequestTimeout(0),
		goakt.WithReentrancyMode(reentrancy.AllowAll),
	).Then(
		func(payload any, failure error) {
			current := owner.creations[input.AgentID]
			if current.creationID != original.creationID || current.activation != original.activation || current.closed {
				reply.Response(Ready{
					Err: ErrCreation,
				})
				return
			}
			if failure == nil {
				verified, ok := payload.(agentactor.Verified)
				if !ok {
					failure = errors.New("agent registry: invalid activation verification reply")
				} else {
					failure = verified.Err
					if failure != nil {
						// Only a confirmed activation mismatch revokes admission; delivery failure is not proof of lost state.
						_ = owner.directory.BeginClose(input.AgentID)
						current.activation.failure = failure
					}
				}
			}
			if failure != nil {
				reply.Response(Ready{
					Err: failure,
				})
				return
			}
			reply.Response(owner.ready(input.AgentID))
		},
	)
}
