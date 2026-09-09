package toolactor

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/tools"
	"github.com/gorenx/goren/tools/tool"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

// Client routes calls to one existing tool creation and committed implementation.
// It retains no caller Actor, callbacks, business execution state or retry policy.
type Client struct {
	target         *goakt.GrainIdentity
	creationID     string
	implementation tool.Identity
	stopped        chan struct{}
}

// CallResult separates a returned body result from an unconfirmed request outcome.
type CallResult struct {
	CallID agentmessage.CallID
	Body   tools.BodyResult
	// Err denotes request failure or an invalid reply; body exit is not confirmed.
	Err error
}

// Cancellation reports only cancellation delivery, never body completion.
type Cancellation struct {
	CallID agentmessage.CallID
	Err    error
}

// ExecutionReceiver consumes a call result during its current Actor turn.
type ExecutionReceiver interface {
	OnToolExecuted(*goakt.GrainContext, CallResult)
}

// CancellationReceiver consumes cancellation delivery during its current Actor turn.
type CancellationReceiver interface {
	OnToolCancellation(*goakt.GrainContext, Cancellation)
}

// Receivers supplies only the handlers needed for the current mailbox message.
type Receivers struct {
	Execution    ExecutionReceiver
	Cancellation CancellationReceiver
}

type callReceived struct {
	endpoint *Client
	result   CallResult
}

type cancellationReceived struct {
	endpoint *Client
	result   Cancellation
}

// NewClient binds an existing endpoint without activating or configuring it.
func NewClient(target *goakt.GrainIdentity, creationID string, implementation tool.Identity) (*Client, error) {
	if target == nil || creationID == "" || implementation == "" {
		return nil, errors.New("tools: tool identity, creation and committed implementation are required")
	}
	err := target.Validate()
	if err != nil {
		return nil, err
	}
	return &Client{
		target:         target,
		creationID:     creationID,
		implementation: implementation,
		stopped:        make(chan struct{}),
	}, nil
}

// Execute asynchronously requests the body. The caller keeps processing messages
// so cancellation can be delivered while this request awaits actual body exit.
func (endpoint *Client) Execute(gc *goakt.GrainContext, ctx context.Context, input tools.Invocation) error {
	input.Arguments = input.ArgumentsJSON()
	received := make(chan any, 1)
	err := endpoint.await(gc, received)
	if err != nil {
		return err
	}
	gc.RequestGrain(endpoint.target, Execute{
		CreationID:     endpoint.creationID,
		Implementation: endpoint.implementation,
		Invocation:     input,
		Context:        ctx,
	}, goakt.WithRequestTimeout(0), goakt.WithReentrancyMode(reentrancy.AllowAll)).Then(func(payload any, failure error) {
		result := CallResult{
			CallID: input.CallID,
			Err:    failure,
		}
		if failure == nil {
			body, ok := payload.(tools.BodyResult)
			if !ok {
				result.Err = errors.New("tools: invalid body reply")
			} else {
				result.Body = body
			}
		}
		received <- callReceived{
			endpoint: endpoint,
			result:   result,
		}
	})
	return nil
}

// Cancel requests cancellation without completing or retrying the original call.
func (endpoint *Client) Cancel(gc *goakt.GrainContext, identifier agentmessage.CallID) error {
	received := make(chan any, 1)
	err := endpoint.await(gc, received)
	if err != nil {
		return err
	}
	gc.RequestGrain(endpoint.target, CancelCall{
		CreationID: endpoint.creationID,
		CallID:     identifier,
	}, goakt.WithReentrancyMode(reentrancy.AllowAll)).Then(func(payload any, failure error) {
		if failure == nil {
			result, ok := payload.(Completion)
			if !ok {
				failure = errors.New("tools: invalid cancellation reply")
			} else {
				failure = result.Err
			}
		}
		received <- cancellationReceived{
			endpoint: endpoint,
			result: Cancellation{
				CallID: identifier,
				Err:    failure,
			},
		}
	})
	return nil
}

// Dispose stops reply-wait tasks when the owning Grain deactivates. Like the
// other Client methods, it is serialized by the owner and is safe to repeat.
// It does not cancel or certify completion of remote tool bodies.
func (endpoint *Client) Dispose() {
	select {
	case <-endpoint.stopped:
	default:
		close(endpoint.stopped)
	}
}

func (endpoint *Client) await(gc *goakt.GrainContext, received <-chan any) error {
	stopped := endpoint.stopped
	select {
	case <-stopped:
		return errors.New("tools: client is disposed")
	default:
	}
	return gc.PipeToSelf(func() (any, error) {
		select {
		case message := <-received:
			return message, nil
		case <-stopped:
			return nil, nil
		}
	})
}

// Receive dispatches this client's results using handlers from the current turn.
func (endpoint *Client) Receive(gc *goakt.GrainContext, handlers Receivers) bool {
	switch input := gc.Message().(type) {
	case callReceived:
		if input.endpoint != endpoint || handlers.Execution == nil {
			return false
		}
		gc.NoErr()
		handlers.Execution.OnToolExecuted(gc, input.result)
	case cancellationReceived:
		if input.endpoint != endpoint || handlers.Cancellation == nil {
			return false
		}
		gc.NoErr()
		handlers.Cancellation.OnToolCancellation(gc, input.result)
	default:
		return false
	}
	return true
}
