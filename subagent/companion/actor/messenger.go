package companionactor

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agent"
	agentregistry "github.com/gorenx/goren/agent/registry"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	companions "github.com/gorenx/goren/subagent/companion"
	"github.com/gorenx/goren/subagent/tools/control"
	"github.com/gorenx/goren/subagent/tools/report"
	"github.com/gorenx/goren/tools"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Messenger routes the shared subagent tools to a Companion's Steward. It has no
// Agent ownership, directory cache, startup side effects or recovery policy.
type Messenger struct {
	// system is borrowed for Registry queries and Steward requests.
	system goakt.ActorSystem
	// authority is the Agent Registry endpoint that verifies the caller and its direct parent.
	authority *goakt.GrainIdentity
}

// NewMessenger binds existing Registry and framework capabilities.
func NewMessenger(system goakt.ActorSystem, authority *goakt.GrainIdentity) (*Messenger, error) {
	if system == nil || authority == nil {
		return nil, companions.ErrInvalid
	}
	return &Messenger{
		system:    system,
		authority: authority,
	}, nil
}

// Send delivers a task with provenance derived from the trusted caller.
// No unavailable child is resumed here.
func (sender *Messenger) Send(ctx context.Context, input control.Message) (agentmessage.MessageID, error) {
	caller, err := callerCreation(ctx, input.ParentSessionID)
	if err != nil {
		return "", err
	}
	endpoint, err := sender.client(ctx, caller)
	if err != nil {
		return "", err
	}
	return endpoint.SendMessage(ctx, input.ChildSessionID, input.Content)
}

// Report resolves only the exact caller's direct parent. The Steward then checks
// that it still owns that child Creation before entering the parent's Inbox.
func (sender *Messenger) Report(ctx context.Context, input report.Report) (agentmessage.MessageID, error) {
	if input.Delivery != report.Quiet {
		return "", errors.New("companion: reports must queue without waking the parent")
	}
	caller, err := callerCreation(ctx, input.ChildSessionID)
	if err != nil {
		return "", err
	}
	inspected, err := ask[agentregistry.Inspected](ctx, sender.system, sender.authority, agentregistry.Inspect{
		Creation: caller,
	})
	if err = errors.Join(err, inspected.Err); err != nil {
		return "", err
	}
	if inspected.Registration.Role != agent.CompanionRole || inspected.Registration.ParentID == nil {
		return "", agent.ErrRole
	}
	parent, err := ask[agentregistry.Ready](ctx, sender.system, sender.authority, agentregistry.FindAgent{
		AgentID: *inspected.Registration.ParentID,
	})
	if err = errors.Join(err, parent.Err); err != nil {
		return "", err
	}
	endpoint, err := sender.client(ctx, parent.Creation)
	if err != nil {
		return "", err
	}
	return endpoint.Report(ctx, caller, input.Output)
}

func (sender *Messenger) client(ctx context.Context, parent agentregistry.Creation) (*Client, error) {
	target, err := stewardRoute(ctx, sender.system, parent)
	if err != nil {
		return nil, err
	}
	return &Client{
		system: sender.system,
		target: target,
		parent: parent,
	}, nil
}

func callerCreation(ctx context.Context, identifier session.SessionID) (agentregistry.Creation, error) {
	lifetime, found := tools.AgentCreationIDFromContext(ctx)
	if !found || identifier == "" {
		return agentregistry.Creation{}, agentregistry.ErrCreation
	}
	if err := ctx.Err(); err != nil {
		return agentregistry.Creation{}, err
	}
	return agentregistry.Creation{
		AgentID:    identifier,
		CreationID: lifetime,
	}, nil
}

var _ control.Sender = (*Messenger)(nil)
var _ report.Reporter = (*Messenger)(nil)
