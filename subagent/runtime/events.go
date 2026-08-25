package runtime

import (
	"context"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/subagent/internal/oneshot"
	providerregistry "github.com/gorenx/goren/subagent/internal/provider"
)

// eventPublisher adapts Subagent lifecycle ports to the Plugin Event bus.
type eventPublisher struct {
	owner *Plugin
}

// Added publishes a vetoable Provider registration fact.
func (publisher *eventPublisher) Added(
	requestContext context.Context,
	candidate subagent.Provider,
) error {
	return plugin.Publish(
		requestContext,
		publisher.owner,
		subagent.ProviderAdded{
			Provider: candidate,
		},
	)
}

// Removed publishes best-effort Provider cleanup.
func (publisher *eventPublisher) Removed(
	requestContext context.Context,
	name string,
) {
	_ = plugin.Publish(
		requestContext,
		publisher.owner,
		subagent.ProviderRemoved{
			Name: name,
		},
	)
}

// Started publishes an accepted Subagent lifecycle fact from the parent Scope.
func (*eventPublisher) Started(parentAgent agent.Agent, fact subagent.Started) {
	_ = agent.DispatchRuntimeEvent(context.Background(), parentAgent, fact)
}

// Ended publishes a terminal Subagent lifecycle fact from the parent Scope.
func (*eventPublisher) Ended(parentAgent agent.Agent, fact subagent.Ended) {
	_ = agent.DispatchRuntimeEvent(context.Background(), parentAgent, fact)
}

// ObserveEvent adapts Agent Inbox and disposal facts to continuation state.
func (owner *Plugin) ObserveEvent(
	_ context.Context,
	fact plugin.Event,
) error {
	switch notice := fact.(type) {
	case agent.InboxClaimed:
		owner.continuations.MessageLeftInbox(
			notice.Subject,
			notice.Message.StableID(),
		)
	case agent.InboxDiscarded:
		owner.continuations.MessageLeftInbox(
			notice.Subject,
			notice.Message.StableID(),
		)
	case agent.Disposed:
		owner.continuations.AgentDisposed(notice.Subject)
	}
	return nil
}

var _ providerregistry.Events = (*eventPublisher)(nil)
var _ oneshot.Lifecycle = (*eventPublisher)(nil)
var _ plugin.EventObserver = (*Plugin)(nil)
