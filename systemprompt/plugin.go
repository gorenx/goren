package systemprompt

import (
	"context"
	"errors"

	"github.com/gorenx/goren/plugin"
)

// RegistryOptions supplies optional global assembly Middleware.
type RegistryOptions struct {
	Middleware plugin.WaterfallMiddleware[AssembleRequest, PromptAssembly]
}

// Registry is the System Prompt Plugin adapter. Its embedded root PromptLayer owns
// prompt state; the adapter owns only Plugin lifecycle and global effects.
type Registry struct {
	plugin.Base
	name       string
	middleware plugin.WaterfallMiddleware[AssembleRequest, PromptAssembly]
	*PromptLayer
}

// New constructs the root System Prompt Plugin from validated configuration.
func New(settings ValidatedConfig, options RegistryOptions) *Registry {
	owner := &Registry{
		name:       PluginName,
		middleware: options.Middleware,
	}
	owner.PromptLayer = &PromptLayer{
		root:     true,
		settings: settings,
		store:    newPromptStore(),
		effects:  owner,
	}
	return owner
}

// NewOverlay constructs an ancestor-decorating System Prompt Plugin.
func NewOverlay(options RegistryOptions) *Registry {
	owner := &Registry{
		name:       OverlayPluginName,
		middleware: options.Middleware,
	}
	owner.PromptLayer = &PromptLayer{
		store:   newPromptStore(),
		effects: owner,
	}
	return owner
}

// Manifest declares prompt capabilities and optional assembly Middleware.
func (owner *Registry) Manifest() plugin.Manifest {
	requiredServices := []plugin.ServiceType(nil)
	if !owner.PromptLayer.root {
		requiredServices = []plugin.ServiceType{
			plugin.ServiceOf[Assembler](),
		}
	}
	waterfalls := []plugin.WaterfallMiddlewareBinding(nil)
	if owner.middleware != nil {
		waterfalls = []plugin.WaterfallMiddlewareBinding{
			plugin.WaterfallOf[AssembleRequest, PromptAssembly](owner.middleware),
		}
	}
	return plugin.Manifest{
		Name: owner.name,
		Provides: []plugin.ProvidedService{
			plugin.NewProvidedService[Assembler](owner),
			plugin.NewProvidedService[PromptRegistry](owner),
			plugin.NewProvidedService[PromptLayerFactory](owner),
		},
		Requires:   requiredServices,
		Waterfalls: waterfalls,
	}
}

// Apply resolves overlay ancestry or installs root built-ins.
func (owner *Registry) Apply(requestContext context.Context) error {
	if err := requestContext.Err(); err != nil {
		return err
	}
	if !owner.PromptLayer.root {
		parentService, err := plugin.Require[Assembler](owner)
		if err != nil {
			return err
		}
		parent, matches := parentService.(layerSource)
		if !matches {
			return errors.New(
				"systemprompt: ancestor Service does not expose prompt layers",
			)
		}
		owner.PromptLayer.parent = parent
		return nil
	}
	if owner.PromptLayer.settings.includeHarnessIdentity {
		if _, err := owner.PromptLayer.store.addSection(PromptSection{
			Name:  harnessIdentityName,
			Order: harnessIdentityOrder,
			Text:  StaticText(harnessIdentityText),
		}); err != nil {
			return err
		}
	}
	if _, err := owner.PromptLayer.store.addSection(PromptSection{
		Name:  PersonaSection,
		Order: PersonaOrder,
		Text:  StaticText(owner.PromptLayer.settings.persona),
	}); err != nil {
		return err
	}
	if !owner.PromptLayer.settings.includeRuntimeContext {
		if _, err := owner.PromptLayer.store.addSuppressor(
			disabledRuntimeContextSuppressor,
		); err != nil {
			return err
		}
	}
	return nil
}

// Dispose releases the embedded root PromptLayer after dependents stop.
func (owner *Registry) Dispose(context.Context) error {
	owner.PromptLayer.close()
	return nil
}

// NewLayer creates one plain child layer inheriting the root prompt view.
func (owner *Registry) NewLayer() *PromptLayer {
	if owner == nil || owner.PromptLayer == nil {
		return nil
	}
	return &PromptLayer{
		store:   newPromptStore(),
		parent:  owner.PromptLayer,
		effects: owner,
	}
}

var _ plugin.Plugin = (*Registry)(nil)
var _ PromptLayerFactory = (*Registry)(nil)
