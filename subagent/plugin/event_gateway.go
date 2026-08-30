package plugin

import (
	"context"
	"errors"
	"sync"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/subagent/internal/seedbuilder"
)

// eventGateway is the business-facing event port shared by Subagent services.
// It owns no Plugin or Runtime reference.
type eventGateway struct {
	requests chan eventRequest
	stopped  chan struct{}
	once     sync.Once
}

type eventRequest interface {
	eventRequest()
}

type seedBuilderAddedRequest struct {
	requestContext context.Context
	builder        subagent.SeedBuilder
	result         chan error
}

func (seedBuilderAddedRequest) eventRequest() {}

type seedBuilderRemovedRequest struct {
	requestContext context.Context
	name           string
	done           chan struct{}
}

func (seedBuilderRemovedRequest) eventRequest() {}

type executionStartedRequest struct {
	parent agent.Agent
	fact   subagent.Started
	done   chan struct{}
}

func (executionStartedRequest) eventRequest() {}

type executionEndedRequest struct {
	parent agent.Agent
	fact   subagent.Ended
	done   chan struct{}
}

func (executionEndedRequest) eventRequest() {}

func newEventGateway() *eventGateway {
	return &eventGateway{
		requests: make(chan eventRequest),
		stopped:  make(chan struct{}),
	}
}

func (gateway *eventGateway) submit(
	requestContext context.Context,
	request eventRequest,
) bool {
	if gateway == nil || request == nil {
		return false
	}
	if requestContext == nil {
		requestContext = context.Background()
	}
	select {
	case gateway.requests <- request:
		return true
	case <-gateway.stopped:
		return false
	case <-requestContext.Done():
		return false
	}
}

func (gateway *eventGateway) PublishAdded(
	requestContext context.Context,
	builder subagent.SeedBuilder,
) error {
	if requestContext == nil {
		return errors.New("subagent: event publication Context is nil")
	}
	result := make(chan error, 1)
	if !gateway.submit(
		requestContext,
		seedBuilderAddedRequest{
			requestContext: requestContext,
			builder:        builder,
			result:         result,
		},
	) {
		return errors.New("subagent: event gateway is unavailable")
	}
	select {
	case err := <-result:
		return err
	case <-gateway.stopped:
		return errors.New("subagent: event gateway stopped")
	case <-requestContext.Done():
		return context.Cause(requestContext)
	}
}

func (gateway *eventGateway) PublishRemoved(
	requestContext context.Context,
	name string,
) {
	if requestContext == nil {
		requestContext = context.Background()
	}
	done := make(chan struct{})
	if !gateway.submit(
		requestContext,
		seedBuilderRemovedRequest{
			requestContext: requestContext,
			name:           name,
			done:           done,
		},
	) {
		return
	}
	waitForEvent(requestContext, gateway.stopped, done)
}

func (gateway *eventGateway) PublishStarted(
	parentAgent agent.Agent,
	fact subagent.Started,
) {
	done := make(chan struct{})
	if !gateway.submit(
		context.Background(),
		executionStartedRequest{
			parent: parentAgent,
			fact:   fact,
			done:   done,
		},
	) {
		return
	}
	waitForEvent(context.Background(), gateway.stopped, done)
}

func (gateway *eventGateway) PublishEnded(
	parentAgent agent.Agent,
	fact subagent.Ended,
) {
	done := make(chan struct{})
	if !gateway.submit(
		context.Background(),
		executionEndedRequest{
			parent: parentAgent,
			fact:   fact,
			done:   done,
		},
	) {
		return
	}
	waitForEvent(context.Background(), gateway.stopped, done)
}

func waitForEvent(
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

func (gateway *eventGateway) stop() {
	if gateway == nil {
		return
	}
	gateway.once.Do(func() {
		close(gateway.stopped)
	})
}

var _ seedbuilder.EventPublisher = (*eventGateway)(nil)
