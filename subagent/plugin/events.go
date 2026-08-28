package plugin

import (
	"context"

	"github.com/gorenx/goren/agent"
	pluginruntime "github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/subagent"
	sharedexecution "github.com/gorenx/goren/subagent/internal/execution"
	"github.com/gorenx/goren/subagent/internal/seedbuilder"
)

// eventPublisher adapts business lifecycle facts to their owning event buses.
type eventPublisher struct {
	owner *Plugin
}

// Added publishes the canonical vetoable registration fact.
func (publisher *eventPublisher) PublishAdded(
	ctx context.Context,
	builder subagent.SeedBuilder,
) error {
	return pluginruntime.Publish(
		ctx,
		publisher.owner,
		subagent.SeedBuilderAdded{
			SeedBuilder: builder,
		},
	)
}

// Removed publishes best-effort registration cleanup.
func (publisher *eventPublisher) PublishRemoved(
	ctx context.Context,
	name string,
) {
	_ = pluginruntime.Publish(
		ctx,
		publisher.owner,
		subagent.SeedBuilderRemoved{
			Name: name,
		},
	)
}

// Started publishes one accepted execution fact in the parent Agent scope.
func (*eventPublisher) PublishStarted(
	parentAgent agent.Agent,
	fact subagent.Started,
) {
	_ = agent.DispatchRuntimeEvent(context.Background(), parentAgent, fact)
}

// Ended publishes the paired terminal fact in the parent Agent scope.
func (*eventPublisher) PublishEnded(
	parentAgent agent.Agent,
	fact subagent.Ended,
) {
	_ = agent.DispatchRuntimeEvent(context.Background(), parentAgent, fact)
}

// ObserveEvent forwards structural Agent closure to the business Service so
// the same Execution terminal transaction completes before teardown continues.
func (owner *Plugin) ObserveEvent(
	ctx context.Context,
	fact pluginruntime.Event,
) error {
	switch observed := fact.(type) {
	case agent.Disposed:
		return owner.service.AgentDisposed(
			context.WithoutCancel(ctx),
			observed.Subject,
		)
	case agent.SessionStarted:
		owner.service.AgentSessionStarted(observed.Subject)
		return nil
	default:
		return nil
	}
}

var _ seedbuilder.EventPublisher = (*eventPublisher)(nil)
var _ sharedexecution.EventPublisher = (*eventPublisher)(nil)
var _ pluginruntime.EventObserver = (*Plugin)(nil)
