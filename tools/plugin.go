package tools

import (
	"context"
	"errors"

	"github.com/gorenx/goren/approval"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/systemprompt"
)

// Service is the Tools Plugin adapter. Its embedded ToolLayer owns Tool state;
// the adapter owns only Plugin lifecycle, dependency resolution, and effects.
type Service struct {
	plugin.Base
	name      string
	root      bool
	settings  ValidatedConfig
	approvals approval.Approval
	*ToolLayer
}

// New constructs the root Tools Plugin from validated configuration.
func New(settings ValidatedConfig) *Service {
	owner := &Service{
		name:     PluginName,
		root:     true,
		settings: settings,
	}
	owner.ToolLayer = &ToolLayer{
		registry: newRegistry(true),
		effects:  owner,
	}
	return owner
}

// NewOverlay constructs an ancestor-decorating Tools Plugin.
func NewOverlay() *Service {
	owner := &Service{
		name: OverlayPluginName,
	}
	owner.ToolLayer = &ToolLayer{
		registry: newRegistry(false),
		effects:  owner,
	}
	return owner
}

// Manifest declares Tool capabilities and their provider dependencies.
func (owner *Service) Manifest() plugin.Manifest {
	requiredServices := []plugin.ServiceType{
		plugin.ServiceOf[systemprompt.PromptRegistry](),
	}
	if !owner.root {
		requiredServices = append(
			requiredServices,
			plugin.ServiceOf[ToolRuntime](),
		)
	}
	return plugin.Manifest{
		Name: owner.name,
		Provides: []plugin.ProvidedService{
			plugin.NewProvidedService[ToolRuntime](owner),
			plugin.NewProvidedService[ToolCatalog](owner),
			plugin.NewProvidedService[PolicyRegistry](owner),
			plugin.NewProvidedService[ToolLayerFactory](owner),
		},
		Requires: requiredServices,
		Optional: []plugin.ServiceType{
			plugin.ServiceOf[approval.Approval](),
		},
	}
}

// Apply resolves dependencies and activates the embedded ToolLayer.
func (owner *Service) Apply(requestContext context.Context) error {
	if err := requestContext.Err(); err != nil {
		return err
	}
	if owner.root && (owner.settings.mode != PresentationNative ||
		owner.settings.maxParallelSubCalls < 1) {
		return errors.New("tools: configuration was not validated")
	}
	if !owner.root {
		parentService, err := plugin.Require[ToolRuntime](owner)
		if err != nil {
			return err
		}
		parent, matches := parentService.(layeredToolRuntime)
		if !matches {
			return errors.New(
				"tools: ancestor Service does not expose Tool layers",
			)
		}
		if err := owner.registry.attachParent(parent); err != nil {
			return err
		}
	}
	approvals, _ := plugin.Resolve[approval.Approval](owner)
	owner.approvals = approvals
	stagedRuntime := newRuntime(owner.ToolLayer, owner.registry, approvals)
	prompts, err := plugin.Require[systemprompt.PromptRegistry](owner)
	if err != nil {
		return err
	}
	promptHandle, err := prompts.AddToolProvider(
		requestContext,
		promptToolProviderName,
		owner,
	)
	if err != nil {
		return err
	}
	owner.prompt = promptHandle
	owner.runtimeMutex.Lock()
	owner.runtime = stagedRuntime
	owner.runtimeMutex.Unlock()
	return nil
}

// Dispose releases the embedded root ToolLayer after dependents stop.
func (owner *Service) Dispose(closeContext context.Context) error {
	owner.approvals = nil
	return owner.ToolLayer.Close(closeContext)
}

// NewLayer creates and activates one plain child ToolLayer.
func (owner *Service) NewLayer(
	requestContext context.Context,
	prompts systemprompt.PromptRegistry,
) (*ToolLayer, error) {
	if owner == nil || owner.ToolLayer == nil || owner.ToolLayer.executionRuntime() == nil {
		return nil, errors.New("tools: root ToolLayer is unavailable")
	}
	if prompts == nil {
		return nil, errors.New("tools: child ToolLayer requires Prompt Registry")
	}
	childRegistry := newRegistry(false)
	if err := childRegistry.attachParent(owner.ToolLayer); err != nil {
		return nil, err
	}
	child := &ToolLayer{
		registry: childRegistry,
		effects:  owner,
	}
	child.runtime = newRuntime(child, childRegistry, owner.approvals)
	promptHandle, err := prompts.AddToolProvider(
		requestContext,
		promptToolProviderName,
		child,
	)
	if err != nil {
		child.runtime = nil
		child.registry.clear()
		return nil, err
	}
	child.prompt = promptHandle
	return child, nil
}

var _ plugin.Plugin = (*Service)(nil)
var _ ToolLayerFactory = (*Service)(nil)
