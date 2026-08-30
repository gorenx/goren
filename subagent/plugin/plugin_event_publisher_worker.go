package plugin

import (
	"context"

	"github.com/gorenx/goren/agent"
	pluginruntime "github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/subagent"
	sharedexecution "github.com/gorenx/goren/subagent/internal/execution"
)

func (owner *Plugin) serveEventPublications(
	publisherRun *eventPublisherRun,
	dispatcher agent.EventDispatcher,
) {
	defer close(publisherRun.done)
	for {
		select {
		case requested := <-owner.events.requests:
			owner.publishEventRequest(requested, dispatcher)
		case <-publisherRun.stopped:
			return
		}
	}
}

func (owner *Plugin) publishEventRequest(
	requested publicationRequest,
	dispatcher agent.EventDispatcher,
) {
	switch request := requested.(type) {
	case seedBuilderAddedPublication:
		request.result <- pluginruntime.Publish(
			request.requestContext,
			owner,
			subagent.SeedBuilderAdded{
				SeedBuilder: request.builder,
			},
		)
	case seedBuilderRemovedPublication:
		_ = pluginruntime.Publish(
			context.WithoutCancel(request.requestContext),
			owner,
			subagent.SeedBuilderRemoved{
				Name: request.name,
			},
		)
		close(request.done)
	case executionPublication:
		if dispatcher != nil {
			_ = dispatcher.Dispatch(
				context.Background(),
				request.parent,
				request.fact,
			)
		}
		close(request.done)
	}
}

// ObserveEvent forwards structural Agent closure to the business Service so
// the same Execution terminal transaction completes before teardown continues.
func (owner *Plugin) ObserveEvent(
	requestContext context.Context,
	fact pluginruntime.Event,
) error {
	switch observed := fact.(type) {
	case agent.Disposed:
		return owner.service.AgentDisposed(
			context.WithoutCancel(requestContext),
			observed.Subject,
		)
	case agent.SessionStarted:
		owner.service.AgentSessionStarted(observed.Subject)
		return nil
	default:
		return nil
	}
}

var _ sharedexecution.EventPublisher = (*subagentEventPublisher)(nil)
var _ pluginruntime.EventObserver = (*Plugin)(nil)
