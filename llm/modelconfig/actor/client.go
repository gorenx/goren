package configactor

import (
	"context"
	"errors"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/llm/modelconfig"
	goakt "github.com/tochemey/goakt/v4/actor"
	"time"
)

// Name is the stable server-local model configuration identity.
const Name = "model-config"

// Client adapts non-actor callers. Actor consumers use RequestGrain directly.
type Client struct {
	system goakt.ActorSystem
}

// NewClient binds the immutable endpoint to the process-owned framework.
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
		return nil, errors.New("modelconfig: request context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	identity, err := goakt.GrainOf[*Grain](ctx, endpoint.system, Name, goakt.WithLongLivedGrain())
	if err != nil {
		return nil, err
	}
	return endpoint.system.AskGrain(ctx, identity, message, time.Duration(1<<63-1))
}

// CurrentConfig reads the latest detached snapshot.
func (endpoint *Client) CurrentConfig(ctx context.Context) (modelconfig.Snapshot, error) {
	return endpoint.Read(ctx, 0)
}

// Read reads the current configuration while checking an optional version.
func (endpoint *Client) Read(ctx context.Context, version uint64) (modelconfig.Snapshot, error) {
	return endpoint.configuration(ctx, Read{
		Version: version,
	})
}

// Replace atomically publishes a complete candidate definition.
func (endpoint *Client) Replace(ctx context.Context, expected uint64, candidate modelconfig.Definition) (modelconfig.Snapshot, error) {
	return endpoint.configuration(ctx, Replace{
		ExpectedVersion: expected,
		Definition:      candidate,
	})
}

func (endpoint *Client) configuration(ctx context.Context, message any) (modelconfig.Snapshot, error) {
	payload, err := endpoint.call(ctx, message)
	if err != nil {
		return modelconfig.Snapshot{}, err
	}
	response, ok := payload.(Configuration)
	if !ok {
		return modelconfig.Snapshot{}, errors.New("modelconfig: invalid configuration response")
	}
	return response.Snapshot, response.Err
}

// Resolve returns the effective parameters for one request.
func (endpoint *Client) Resolve(ctx context.Context, version uint64, overrides llm.CallConfig) (modelconfig.Resolved, error) {
	payload, err := endpoint.call(ctx, Resolve{
		Version:   version,
		Overrides: overrides,
	})
	if err != nil {
		return modelconfig.Resolved{}, err
	}
	response, ok := payload.(Resolution)
	if !ok {
		return modelconfig.Resolved{}, errors.New("modelconfig: invalid resolution response")
	}
	return response.Resolved, response.Err
}

var _ modelconfig.Reader = (*Client)(nil)
