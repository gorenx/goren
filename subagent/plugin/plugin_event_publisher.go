package plugin

import (
	"context"
	"errors"
	"sync"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/subagent/internal/seedbuilder"
)

// subagentEventPublisher publishes SeedBuilder registration facts through the
// Plugin Runtime and Subagent Execution lifecycle facts through the exact
// parent Agent Scope. It owns no Plugin or Runtime reference.
type subagentEventPublisher struct {
	requests chan publicationRequest
	mutex    sync.RWMutex
	active   *eventPublisherRun
}

// eventPublisherRun joins one active publication worker with its stop and
// completion signals. It owns no Plugin, Runtime, or business service reference.
type eventPublisherRun struct {
	stopped chan struct{}
	done    chan struct{}
}

type publicationRequest interface {
	publicationRequest()
}

type seedBuilderAddedPublication struct {
	requestContext context.Context
	builder        subagent.SeedBuilder
	result         chan error
}

func (seedBuilderAddedPublication) publicationRequest() {}

type seedBuilderRemovedPublication struct {
	requestContext context.Context
	name           string
	done           chan struct{}
}

func (seedBuilderRemovedPublication) publicationRequest() {}

type executionPublication struct {
	parent agent.Agent
	fact   agent.AgentEvent
	done   chan struct{}
}

func (executionPublication) publicationRequest() {}

func newSubagentEventPublisher() *subagentEventPublisher {
	return &subagentEventPublisher{
		requests: make(chan publicationRequest),
	}
}

func (publisher *subagentEventPublisher) start() (*eventPublisherRun, error) {
	if publisher == nil {
		return nil, errors.New("subagent: event publisher is unavailable")
	}
	publisher.mutex.Lock()
	defer publisher.mutex.Unlock()
	if publisher.active != nil {
		return nil, errors.New("subagent: event publisher is already active")
	}
	publisherRun := &eventPublisherRun{
		stopped: make(chan struct{}),
		done:    make(chan struct{}),
	}
	publisher.active = publisherRun
	return publisherRun, nil
}

func (publisher *subagentEventPublisher) submit(
	requestContext context.Context,
	request publicationRequest,
) *eventPublisherRun {
	if publisher == nil || request == nil {
		return nil
	}
	if requestContext == nil {
		requestContext = context.Background()
	}
	publisher.mutex.RLock()
	publisherRun := publisher.active
	publisher.mutex.RUnlock()
	if publisherRun == nil {
		return nil
	}
	select {
	case publisher.requests <- request:
		return publisherRun
	case <-publisherRun.stopped:
		return nil
	case <-requestContext.Done():
		return nil
	}
}

func (publisher *subagentEventPublisher) PublishAdded(
	requestContext context.Context,
	builder subagent.SeedBuilder,
) error {
	if requestContext == nil {
		return errors.New("subagent: event publication Context is nil")
	}
	result := make(chan error, 1)
	publisherRun := publisher.submit(
		requestContext,
		seedBuilderAddedPublication{
			requestContext: requestContext,
			builder:        builder,
			result:         result,
		},
	)
	if publisherRun == nil {
		return errors.New("subagent: event publisher is unavailable")
	}
	select {
	case err := <-result:
		return err
	case <-publisherRun.stopped:
		return errors.New("subagent: event publisher stopped")
	case <-requestContext.Done():
		return context.Cause(requestContext)
	}
}

func (publisher *subagentEventPublisher) PublishRemoved(
	requestContext context.Context,
	name string,
) {
	if requestContext == nil {
		requestContext = context.Background()
	}
	done := make(chan struct{})
	publisherRun := publisher.submit(
		requestContext,
		seedBuilderRemovedPublication{
			requestContext: requestContext,
			name:           name,
			done:           done,
		},
	)
	if publisherRun == nil {
		return
	}
	waitForPublication(requestContext, publisherRun.stopped, done)
}

func (publisher *subagentEventPublisher) PublishStarted(
	parentAgent agent.Agent,
	fact subagent.Started,
) {
	publisher.publishExecutionEvent(parentAgent, fact)
}

func (publisher *subagentEventPublisher) PublishEnded(
	parentAgent agent.Agent,
	fact subagent.Ended,
) {
	publisher.publishExecutionEvent(parentAgent, fact)
}

func (publisher *subagentEventPublisher) publishExecutionEvent(
	parentAgent agent.Agent,
	fact agent.AgentEvent,
) {
	done := make(chan struct{})
	publisherRun := publisher.submit(
		context.Background(),
		executionPublication{
			parent: parentAgent,
			fact:   fact,
			done:   done,
		},
	)
	if publisherRun == nil {
		return
	}
	waitForPublication(context.Background(), publisherRun.stopped, done)
}

func waitForPublication(
	requestContext context.Context,
	stopped <-chan struct{},
	done <-chan struct{},
) {
	select {
	case <-done:
	case <-stopped:
	case <-requestContext.Done():
	}
}

func (publisher *subagentEventPublisher) stop() *eventPublisherRun {
	if publisher == nil {
		return nil
	}
	publisher.mutex.Lock()
	publisherRun := publisher.active
	publisher.active = nil
	publisher.mutex.Unlock()
	if publisherRun != nil {
		close(publisherRun.stopped)
	}
	return publisherRun
}

var _ seedbuilder.EventPublisher = (*subagentEventPublisher)(nil)
