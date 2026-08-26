package runtime

import (
	"context"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/subagent/internal/continuable"
	"github.com/gorenx/goren/subagent/internal/oneshot"
	"github.com/gorenx/goren/subagent/internal/seedbuilder"
)

// eventPublisher adapts business lifecycle facts to their owning event buses.
type eventPublisher struct {
	owner *Plugin
}

// Added publishes the canonical vetoable registration fact.
func (publisher *eventPublisher) Added(
	requestContext context.Context,
	builder subagent.SeedBuilder,
) error {
	return plugin.Publish(
		requestContext,
		publisher.owner,
		subagent.SeedBuilderAdded{
			SeedBuilder: builder,
		},
	)
}

// Removed publishes best-effort registration cleanup.
func (publisher *eventPublisher) Removed(
	requestContext context.Context,
	name string,
) {
	_ = plugin.Publish(
		requestContext,
		publisher.owner,
		subagent.SeedBuilderRemoved{
			Name: name,
		},
	)
}

// Started publishes one accepted execution fact in the parent Agent scope.
func (*eventPublisher) Started(parentAgent agent.Agent, fact subagent.Started) {
	_ = agent.DispatchRuntimeEvent(context.Background(), parentAgent, fact)
}

// Ended publishes the paired terminal fact in the parent Agent scope.
func (*eventPublisher) Ended(parentAgent agent.Agent, fact subagent.Ended) {
	_ = agent.DispatchRuntimeEvent(context.Background(), parentAgent, fact)
}

// ObserveEvent forwards structural Agent closure to the business Service so
// the same Execution terminal transaction completes before teardown continues.
func (owner *Plugin) ObserveEvent(
	requestContext context.Context,
	fact plugin.Event,
) error {
	disposed, matches := fact.(agent.Disposed)
	if !matches {
		return nil
	}
	return owner.service.AgentDisposed(
		context.WithoutCancel(requestContext),
		disposed.Subject,
	)
}

var _ seedbuilder.Events = (*eventPublisher)(nil)
var _ oneshot.Lifecycle = (*eventPublisher)(nil)
var _ continuable.Lifecycle = (*eventPublisher)(nil)
var _ plugin.EventObserver = (*Plugin)(nil)
