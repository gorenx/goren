package tools

import (
	"context"
	"errors"

	"github.com/gorenx/goren/approval"
)

// PrePolicy decides whether one validated execution may proceed to dispatch.
type PrePolicy interface {
	ResolvePreExecute(context.Context, PreExecuteRequest) (PreExecuteOutcome, error)
}

// PostPolicy decides how a normalized execution result is presented.
type PostPolicy interface {
	ResolvePostExecute(context.Context, PostExecuteRequest) (PostExecuteOutcome, error)
}

// RegistryObserver observes one committed local catalog mutation.
type RegistryObserver interface {
	RegistryChanged(context.Context) error
}

// Options supplies explicit business dependencies; it carries no framework owner.
type Options struct {
	Before    PrePolicy
	After     PostPolicy
	Execution ExecuteMiddleware
	Results   ResultObserver
	Changes   RegistryObserver
	Approvals approval.Approval
}

// New constructs a ready tool layer from validated configuration.
func New(settings ValidatedConfig, options Options) (*ToolLayer, error) {
	if settings.mode != PresentationNative || settings.maxParallelSubCalls < 1 {
		return nil, errors.New("tools: configuration was not validated")
	}
	return newToolLayer(newRegistry(true), options), nil
}

func newToolLayer(catalog *registry, options Options) *ToolLayer {
	stages := &pipeline{
		options: options,
	}
	return &ToolLayer{
		registry: catalog,
		pipeline: stages,
		runtime:  newRuntime(stages, catalog, options.Approvals),
	}
}

// NewLayer captures the current root definitions and creates local execution state.
func (owner *ToolLayer) NewLayer(ctx context.Context) (*ToolLayer, error) {
	if err := validateMutationContext(ctx); err != nil {
		return nil, err
	}
	if err := owner.requireActive(); err != nil {
		return nil, err
	}
	catalog := newRegistry(false)
	if err := catalog.setBase(owner.registry.toolLayers()); err != nil {
		return nil, err
	}
	return newToolLayer(catalog, owner.pipeline.options), nil
}
