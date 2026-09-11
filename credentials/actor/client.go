package credentialactor

import (
	"context"
	"errors"
	"time"

	"github.com/gorenx/goren/credentials"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Client adapts non-actor callers to requests. Never call it inside a mailbox;
// actor callers use RequestGrain and the messages in this package directly.
type Client struct {
	system goakt.ActorSystem
}

// Name is the server-local Credentials Grain identity key.
const Name = "credentials"

// NewClient fixes the framework endpoint for this client's lifetime.
func NewClient(running goakt.ActorSystem) (*Client, error) {
	if running == nil {
		return nil, ErrUnavailable
	}
	return &Client{
		system: running,
	}, nil
}

func (endpoint *Client) call(ctx context.Context, message any) (any, error) {
	if ctx == nil {
		return nil, errors.New("credentials: request context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	identity, err := goakt.GrainOf[*Grain](ctx, endpoint.system, Name, goakt.WithLongLivedGrain())
	if err != nil {
		return nil, err
	}
	// Keep the caller's context as the deadline authority; do not introduce
	// a shorter default timeout into the existing Provider contract.
	return endpoint.system.AskGrain(ctx, identity, message, time.Duration(1<<63-1))
}

// Resolve waits for one provider credential operation at a non-actor boundary.
func (endpoint *Client) Resolve(ctx context.Context, credentialRef string) (credentials.Resolved, error) {
	payload, err := endpoint.call(ctx, Resolve{
		Context: ctx,
		Ref:     credentialRef,
	})
	if err != nil {
		return credentials.Resolved{}, err
	}
	response, ok := payload.(Resolution)
	if !ok {
		return credentials.Resolved{}, errors.New("credentials: invalid resolution response")
	}
	return response.Resolved, response.Err
}

// Describe waits for value-free metadata at a non-actor boundary.
func (endpoint *Client) Describe(ctx context.Context, credentialRef string) (credentials.Info, error) {
	payload, err := endpoint.call(ctx, Describe{
		Context: ctx,
		Ref:     credentialRef,
	})
	if err != nil {
		return credentials.Info{}, err
	}
	response, ok := payload.(Description)
	if !ok {
		return credentials.Info{}, errors.New("credentials: invalid description response")
	}
	return response.Info, response.Err
}

// Set waits for the actual write result, subject to the caller's cancellation.
func (endpoint *Client) Set(ctx context.Context, credentialRef string, value string) error {
	return endpoint.complete(ctx, Set{
		Context: ctx,
		Ref:     credentialRef,
		Value:   value,
	})
}

// Unset waits for the actual removal result.
func (endpoint *Client) Unset(ctx context.Context, credentialRef string) error {
	return endpoint.complete(ctx, Unset{
		Context: ctx,
		Ref:     credentialRef,
	})
}

func (endpoint *Client) complete(ctx context.Context, message any) error {
	payload, err := endpoint.call(ctx, message)
	if err != nil {
		return err
	}
	response, ok := payload.(Completion)
	if !ok {
		return errors.New("credentials: invalid completion response")
	}
	return response.Err
}

var _ credentials.Provider = (*Client)(nil)
