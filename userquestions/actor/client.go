package questionactor

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/gorenx/goren/userquestions"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Client is the blocking tool-body adapter. Invoke it outside Actor Receive;
// the framework request remains pending until the provider has cleaned up.
type Client struct {
	system goakt.ActorSystem
	target *goakt.GrainIdentity
}

// NewClient binds an existing question Grain.
func NewClient(system goakt.ActorSystem, target *goakt.GrainIdentity) (*Client, error) {
	if system == nil || target == nil {
		return nil, errors.New("userquestions: actor endpoint is required")
	}
	return &Client{
		system: system,
		target: target,
	}, nil
}

// Ask propagates caller cancellation but waits for the original question result.
func (endpoint *Client) Ask(ctx context.Context, input userquestions.Request) (userquestions.Answer, error) {
	if ctx == nil {
		return userquestions.Answer{}, errors.New("userquestions: context is required")
	}
	if ctx.Err() != nil {
		return userquestions.Answer{}, context.Cause(ctx)
	}

	requestContext, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	result := make(answer, 1)
	err := endpoint.system.TellGrain(context.WithoutCancel(ctx), endpoint.target, Ask{
		ID:       uuid.NewString(),
		Context:  requestContext,
		Request:  input,
		Receiver: result,
	})
	if err != nil {
		cancel(err)
		return userquestions.Answer{}, err
	}
	resolved := <-result
	return resolved.Answer, resolved.Err
}

type answer chan Result

func (target answer) Deliver(result Result) { target <- result }
