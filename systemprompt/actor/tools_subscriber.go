package promptactor

import (
	"context"
	"errors"

	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/systemprompt"
	"github.com/gorenx/goren/tools"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// ToolsSubscribed confirms topic membership, not receipt of an authoritative baseline.
type ToolsSubscribed struct {
	AgentID session.SessionID
}

// ToolsApplied reports settlement of a forwarded tool view.
type toolsApplied struct{ err error }

type ToolsApplied struct {
	AgentID session.SessionID
	Err     error
}

// UnsubscribeTools stops forwarding and requests topic removal.
type UnsubscribeTools struct{}

// ToolsUnsubscribed confirms topic removal, not completion of forwarded requests.
type ToolsUnsubscribed struct {
	AgentID session.SessionID
}

// ToolsSubscriber forwards one Agent's committed tool views without owning a catalog.
// Spawn it with RequestGrain reentrancy enabled; its subscriber reads its baseline after topic membership is acknowledged.
type ToolsSubscriber struct {
	creationID  string
	agentID     session.SessionID
	target      *goakt.GrainIdentity
	registry    *goakt.GrainIdentity
	coordinator *goakt.PID
	stopping    bool
	// refreshing spans the current Registry read and its Runtime application.
	// refreshPending coalesces notifications received during that request pair.
	refreshing     bool
	refreshPending bool
}

// NewToolsSubscriber binds framework addresses without starting a subscription.
func NewToolsSubscriber(identifier session.SessionID, target *goakt.GrainIdentity, registry *goakt.GrainIdentity, coordinator *goakt.PID, creationID string) (*ToolsSubscriber, error) {
	if identifier == "" || target == nil || registry == nil || coordinator == nil {
		return nil, errors.New("systemprompt: subscriber Agent, target, Registry and coordinator are required")
	}
	return &ToolsSubscriber{
		creationID:  creationID,
		agentID:     identifier,
		target:      target,
		registry:    registry,
		coordinator: coordinator,
	}, nil
}

// PreStart leaves topic registration to the mailbox's PostStart message.
func (*ToolsSubscriber) PreStart(*goakt.Context) error { return nil }

// PostStop relies on TopicActor's subscriber monitoring for forced removal.
func (*ToolsSubscriber) PostStop(*goakt.Context) error { return nil }

// Receive separates topic acknowledgement from completed Runtime updates.
func (owner *ToolsSubscriber) Receive(rc *goakt.ReceiveContext) {
	switch incoming := rc.Message().(type) {
	case *goakt.PostStart:
		rc.Tell(rc.ActorSystem().TopicActor(), goakt.NewSubscribe(tools.DefinitionsTopic))
	case *goakt.SubscribeAck:
		if incoming.Topic() == tools.DefinitionsTopic && !owner.stopping {
			rc.Tell(owner.coordinator, ToolsSubscribed{
				AgentID: owner.agentID,
			})
			owner.refreshTools(rc)
		}
	case RefreshTools:
		if !owner.stopping {
			owner.refreshTools(rc)
		}
	case toolsBaseline:
		if owner.stopping {
			return
		}
		if incoming.err != nil {
			owner.finishRefresh(rc, incoming.err)
			return
		}
		owner.applyTools(rc, incoming.definitions)
	case tools.Definitions:
		if owner.stopping || incoming.AgentID != owner.agentID {
			return
		}
		owner.refreshTools(rc)
	case toolsApplied:
		owner.finishRefresh(rc, incoming.err)
	case UnsubscribeTools:
		if !owner.stopping {
			owner.stopping = true
			rc.Tell(rc.ActorSystem().TopicActor(), goakt.NewUnsubscribe(tools.DefinitionsTopic))
		}
	case *goakt.UnsubscribeAck:
		if incoming.Topic() == tools.DefinitionsTopic && owner.stopping {
			rc.Tell(owner.coordinator, ToolsUnsubscribed{
				AgentID: owner.agentID,
			})
		}
	default:
		rc.Unhandled()
	}
}

func (owner *ToolsSubscriber) applyTools(rc *goakt.ReceiveContext, incoming tools.Definitions) {
	request := rc.RequestGrain(owner.target, ApplyTools{
		AgentID: incoming.AgentID,
		Definitions: systemprompt.ToolDefinitions{
			Schemas:    incoming.Schemas,
			KnownNames: incoming.KnownNames,
		},
	})
	if request == nil {
		owner.finishRefresh(rc, ErrUnavailable)
		return
	}
	ctx := context.WithoutCancel(rc.Context())
	self := rc.Self()
	logger := rc.Logger()
	request.Then(func(payload any, err error) {
		if err == nil {
			completion, ok := payload.(Completion)
			if !ok {
				err = errors.New("systemprompt: invalid tool update response")
			} else {
				err = completion.Err
			}
		}
		if deliveryErr := self.Tell(ctx, self, toolsApplied{
			err: err,
		}); deliveryErr != nil {
			logger.Error("systemprompt: failed to deliver tool update result to coordinator")
		}
	})
}

func (owner *ToolsSubscriber) finishRefresh(rc *goakt.ReceiveContext, failure error) {
	owner.refreshing = false
	rc.Tell(owner.coordinator, ToolsApplied{
		AgentID: owner.agentID,
		Err:     failure,
	})
	if owner.refreshPending && !owner.stopping {
		owner.refreshPending = false
		owner.refreshTools(rc)
	}
}
