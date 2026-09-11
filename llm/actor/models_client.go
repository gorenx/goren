package llmactor

import (
	"context"
	"errors"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/llm/modelconfig"
	goakt "github.com/tochemey/goakt/v4/actor"
	"time"
)

// Name is the stable server-local model configuration identity.
const Name = "llm"

// ModelsClient adapts non-actor callers. Actor consumers use RequestGrain directly.
type ModelsClient struct {
	system goakt.ActorSystem
}

// NewModelsClient binds the immutable endpoint to the process-owned framework.
func NewModelsClient(running goakt.ActorSystem) (*ModelsClient, error) {
	if running == nil {
		return nil, ErrModelsUnavailable
	}
	return &ModelsClient{
		system: running,
	}, nil
}

func (endpoint *ModelsClient) call(ctx context.Context, message any) (any, error) {
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
func (endpoint *ModelsClient) CurrentConfig(ctx context.Context) (modelconfig.Snapshot, error) {
	return endpoint.Read(ctx, 0)
}

// ReadModels reads the current configuration while checking an optional version.
func (endpoint *ModelsClient) Read(ctx context.Context, version uint64) (modelconfig.Snapshot, error) {
	return endpoint.configuration(ctx, ReadModels{
		Version: version,
	})
}

// ReplaceModels atomically publishes a complete candidate definition.
func (endpoint *ModelsClient) Replace(ctx context.Context, expected uint64, candidate modelconfig.Definition) (modelconfig.Snapshot, error) {
	return endpoint.configuration(ctx, ReplaceModels{
		ExpectedVersion: expected,
		Definition:      candidate,
	})
}

func (endpoint *ModelsClient) configuration(ctx context.Context, message any) (modelconfig.Snapshot, error) {
	payload, err := endpoint.call(ctx, message)
	if err != nil {
		return modelconfig.Snapshot{}, err
	}
	response, ok := payload.(Models)
	if !ok {
		return modelconfig.Snapshot{}, errors.New("modelconfig: invalid configuration response")
	}
	return response.Snapshot, response.Err
}

// ResolveAgent returns the effective parameters for one request.
func (endpoint *ModelsClient) Resolve(ctx context.Context, version uint64, overrides llm.CallConfig) (modelconfig.Resolved, error) {
	payload, err := endpoint.call(ctx, ResolveAgent{
		Version:   version,
		Overrides: overrides,
	})
	if err != nil {
		return modelconfig.Resolved{}, err
	}
	response, ok := payload.(AgentConfiguration)
	if !ok {
		return modelconfig.Resolved{}, errors.New("modelconfig: invalid resolution response")
	}
	return response.Resolved, response.Err
}

var _ modelconfig.Reader = (*ModelsClient)(nil)
