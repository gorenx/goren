package companionactor

import (
	"context"
	"errors"
	"slices"

	"github.com/gorenx/goren/agent"
	agentregistry "github.com/gorenx/goren/agent/registry"
	loopactor "github.com/gorenx/goren/agentloop/actor"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	companions "github.com/gorenx/goren/subagent/companion"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// delivery retains only an ordinary in-flight request, never task execution state.
type delivery struct {
	// cancel bounds identity checks and the Inbox request without retracting committed messages.
	cancel context.CancelFunc
	// reply awaits ordinary Inbox admission on behalf of the tool or Client caller.
	reply *goakt.GrainReply
}

type delivered struct {
	// operation identifies the delivery being removed from Steward's in-flight requests.
	operation *delivery
	// messageID confirms Inbox admission, not child completion or parent consumption of a report.
	messageID agentmessage.MessageID
	// err carries identity-check or ordinary delivery failure back to the caller.
	err error
}

func (owner *steward) findChild(identifier session.SessionID) *companion {
	for _, entry := range owner.entries {
		if entry.relationship.ChildSessionID == identifier {
			return entry
		}
	}
	return nil
}

func (owner *steward) sendMessage(gc *goakt.GrainContext, input sendMessage) {
	entry := owner.findChild(input.child)
	if entry == nil {
		gc.Response(response{
			err: agentregistry.ErrCreation,
		})
		return
	}
	if entry.phase != Ready || entry.relationship.Removed || !entry.relationship.Enabled {
		gc.Response(response{
			err: errors.New("companion: child is unavailable for new tasks"),
		})
		return
	}
	owner.deliver(gc, input.ctx, entry.creation, entry.creation, input.message, loopactor.Followup{
		CreationID: entry.creation.CreationID,
		Message:    input.message,
	})
}

func (owner *steward) reportMessage(gc *goakt.GrainContext, input reportMessage) {
	entry := owner.findChild(input.child.AgentID)
	if entry == nil || entry.creation != input.child {
		gc.Response(response{
			err: agentregistry.ErrCreation,
		})
		return
	}
	if entry.phase != Ready && entry.phase != Starting && entry.phase != Draining {
		gc.Response(response{
			err: agent.ErrClosing,
		})
		return
	}
	owner.deliver(gc, input.ctx, input.child, owner.parent, input.message, loopactor.Inject{
		CreationID: owner.parent.CreationID,
		Message:    input.message,
	})
}

func (owner *steward) deliver(gc *goakt.GrainContext, ctx context.Context, child, target agentregistry.Creation, message agentmessage.UserMessage, input any) {
	if ctx == nil {
		gc.Response(response{
			err: companions.ErrInvalid,
		})
		return
	}
	if err := ctx.Err(); err != nil {
		gc.Response(response{
			err: err,
		})
		return
	}
	reply := gc.DeferResponse()
	if reply == nil {
		gc.Response(response{
			err: errors.New("companion: delivery requires a request"),
		})
		return
	}
	work, cancel := context.WithCancel(ctx)
	operation := &delivery{
		cancel: cancel,
		reply:  reply,
	}
	if owner.deliveries == nil {
		owner.deliveries = make(map[*delivery]struct{})
	}
	owner.deliveries[operation] = struct{}{}
	system := gc.ActorSystem()
	authority := owner.registry
	parent := owner.parent
	err := gc.PipeToSelf(func() (any, error) {
		result := delivered{
			operation: operation,
		}
		var destination *goakt.GrainIdentity
		for _, expected := range []agentregistry.Creation{parent, child} {
			ready, failure := ask[agentregistry.Ready](work, system, authority, agentregistry.FindAgent{
				AgentID: expected.AgentID,
			})
			result.err = errors.Join(failure, ready.Err)
			if result.err == nil && ready.Creation != expected {
				result.err = agentregistry.ErrCreation
			}
			if result.err == nil && expected == child && (ready.Registration.Role != agent.CompanionRole || ready.Registration.ParentID == nil || *ready.Registration.ParentID != parent.AgentID) {
				result.err = agent.ErrRole
			}
			if result.err != nil {
				return result, nil
			}
			if expected == target {
				destination = ready.Executor
			}
		}
		changed, failure := ask[loopactor.Changed](work, system, destination, input)
		result.err = errors.Join(failure, changed.Err)
		if result.err == nil {
			if slices.ContainsFunc(changed.Changes.Inserted, func(entry agentmessage.UserMessage) bool { return entry.StableID() == message.StableID() }) {
				result.messageID = message.StableID()
			} else {
				result.err = errors.New("companion: Inbox did not confirm message acceptance")
			}
		}
		return result, nil
	})
	if err != nil {
		owner.delivered(delivered{
			operation: operation,
			err:       err,
		})
	}
}

func (owner *steward) delivered(result delivered) {
	if _, pending := owner.deliveries[result.operation]; !pending {
		return
	}
	delete(owner.deliveries, result.operation)
	result.operation.cancel()
	result.operation.reply.Response(response{
		messageID: result.messageID,
		err:       result.err,
	})
	owner.finishClose()
}
