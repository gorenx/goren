package systemprompt

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"

	"github.com/gorenx/goren/plugin"
)

var variableNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

const disabledRuntimeContextSuppressor = "configuration:runtime-context-disabled"

type layerSource interface {
	Assembler
	promptLayers() []promptLayerSnapshot
	configuredToolOrder() []string
}

// RegistryOptions supplies an optional assembly Middleware owned by this
// exact System Prompt layer.
type RegistryOptions struct {
	Middleware plugin.WaterfallMiddleware[AssembleRequest, PromptAssembly]
}

// Registry is a System Prompt Service Plugin and the owner of one exact prompt
// layer. An overlay Registry decorates the visible ancestor SystemPrompt.
type Registry struct {
	plugin.Base
	name       string
	root       bool
	settings   ValidatedConfig
	store      *promptStore
	parent     layerSource
	middleware plugin.WaterfallMiddleware[AssembleRequest, PromptAssembly]
}

// New constructs the root System Prompt Plugin without performing lifecycle
// work. Configuration must already be validated by the owning Factory.
func New(settings ValidatedConfig, options RegistryOptions) *Registry {
	return &Registry{
		name:       PluginName,
		root:       true,
		settings:   settings,
		store:      newPromptStore(),
		middleware: options.Middleware,
	}
}

// NewOverlay constructs a child System Prompt layer. During Apply it resolves
// the nearest ancestor SystemPrompt and then provides the same Service type in
// its exact child Scope.
func NewOverlay(options RegistryOptions) *Registry {
	return &Registry{
		name:       OverlayPluginName,
		store:      newPromptStore(),
		middleware: options.Middleware,
	}
}

// Manifest declares the root provider or ancestor-decorating overlay.
func (owner *Registry) Manifest() plugin.Manifest {
	requiredServices := []plugin.ServiceType(nil)
	if !owner.root {
		requiredServices = []plugin.ServiceType{
			plugin.ServiceOf[Assembler](),
		}
	}
	waterfalls := []plugin.WaterfallContribution(nil)
	if owner.middleware != nil {
		waterfalls = []plugin.WaterfallContribution{
			plugin.WaterfallOf[AssembleRequest, PromptAssembly](owner.middleware),
		}
	}
	return plugin.Manifest{
		Name: owner.name,
		Provides: []plugin.ServiceType{
			plugin.ServiceOf[Assembler](),
			plugin.ServiceOf[Contributions](),
		},
		Requires:   requiredServices,
		Waterfalls: waterfalls,
	}
}

// Apply resolves an overlay's ancestor and installs root built-ins into this
// layer before the Service becomes visible.
func (owner *Registry) Apply(requestContext context.Context) error {
	if err := requestContext.Err(); err != nil {
		return err
	}
	if !owner.root {
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
		owner.parent = parent
		return nil
	}
	if owner.settings.includeHarnessIdentity {
		if err := owner.store.addSection(PromptSection{
			Name:  harnessIdentityName,
			Order: harnessIdentityOrder,
			Text:  StaticText(harnessIdentityText),
		}); err != nil {
			return err
		}
	}
	if err := owner.store.addSection(PromptSection{
		Name:  PersonaSection,
		Order: PersonaOrder,
		Text:  StaticText(owner.settings.persona),
	}); err != nil {
		return err
	}
	if !owner.settings.includeRuntimeContext {
		if err := owner.store.addSuppressor(disabledRuntimeContextSuppressor); err != nil {
			return err
		}
	}
	return nil
}

// Dispose discards this layer after Runtime has stopped all dependents.
func (owner *Registry) Dispose(context.Context) error {
	owner.store.clear()
	owner.parent = nil
	return nil
}

// AddSection adds one named section to this exact layer.
func (owner *Registry) AddSection(
	requestContext context.Context,
	definition PromptSection,
) error {
	if err := validateEntryName("prompt section", definition.Name); err != nil {
		return err
	}
	if definition.Text == nil {
		return fmt.Errorf(
			"systemprompt: prompt section %q text provider is nil",
			definition.Name,
		)
	}
	if math.IsNaN(definition.Order) || math.IsInf(definition.Order, 0) {
		return fmt.Errorf(
			"systemprompt: prompt section %q order must be a finite number",
			definition.Name,
		)
	}
	if err := owner.store.addSection(definition); err != nil {
		return err
	}
	if err := owner.publishChanged(requestContext); err != nil {
		owner.store.removeSection(definition.Name)
		return err
	}
	return nil
}

// RemoveSection removes one section owned by this exact layer.
func (owner *Registry) RemoveSection(
	requestContext context.Context,
	name string,
) error {
	if !owner.store.removeSection(name) {
		return nil
	}
	return owner.publishChanged(requestContext)
}

// AddContext adds one named runtime-context provider to this exact layer.
func (owner *Registry) AddContext(
	requestContext context.Context,
	definition PromptContext,
) error {
	if err := validateEntryName("prompt context", definition.Name); err != nil {
		return err
	}
	if definition.Text == nil {
		return fmt.Errorf(
			"systemprompt: prompt context %q text provider is nil",
			definition.Name,
		)
	}
	if math.IsNaN(definition.Order) || math.IsInf(definition.Order, 0) {
		return fmt.Errorf(
			"systemprompt: prompt context %q order must be a finite number",
			definition.Name,
		)
	}
	if err := owner.store.addContext(definition); err != nil {
		return err
	}
	if err := owner.publishChanged(requestContext); err != nil {
		owner.store.removeContext(definition.Name)
		return err
	}
	return nil
}

// RemoveContext removes one runtime-context provider from this exact layer.
func (owner *Registry) RemoveContext(
	requestContext context.Context,
	name string,
) error {
	if !owner.store.removeContext(name) {
		return nil
	}
	return owner.publishChanged(requestContext)
}

// AddRuntimeContextSuppressor disables all runtime context for this layer and
// its descendants until the same named suppressor is removed.
func (owner *Registry) AddRuntimeContextSuppressor(
	requestContext context.Context,
	name string,
) error {
	if err := validateEntryName("runtime-context suppressor", name); err != nil {
		return err
	}
	if err := owner.store.addSuppressor(name); err != nil {
		return err
	}
	if err := owner.publishChanged(requestContext); err != nil {
		owner.store.removeSuppressor(name)
		return err
	}
	return nil
}

// RemoveRuntimeContextSuppressor removes one named suppressor.
func (owner *Registry) RemoveRuntimeContextSuppressor(
	requestContext context.Context,
	name string,
) error {
	if !owner.store.removeSuppressor(name) {
		return nil
	}
	return owner.publishChanged(requestContext)
}

// AddToolProvider adds one named assembly-time tool-schema provider.
func (owner *Registry) AddToolProvider(
	requestContext context.Context,
	name string,
	provider ToolProvider,
) error {
	if err := validateEntryName("tool provider", name); err != nil {
		return err
	}
	if provider == nil {
		return fmt.Errorf("systemprompt: tool provider %q is nil", name)
	}
	if err := owner.store.addToolProvider(name, provider); err != nil {
		return err
	}
	if err := owner.publishChanged(requestContext); err != nil {
		owner.store.removeToolProvider(name)
		return err
	}
	return nil
}

// RemoveToolProvider removes one named provider from this exact layer.
func (owner *Registry) RemoveToolProvider(
	requestContext context.Context,
	name string,
) error {
	if !owner.store.removeToolProvider(name) {
		return nil
	}
	return owner.publishChanged(requestContext)
}

// AddVariable adds one named variable provider to this exact layer.
func (owner *Registry) AddVariable(
	requestContext context.Context,
	name string,
	provider VariableProvider,
) error {
	if !variableNamePattern.MatchString(name) {
		return fmt.Errorf(
			"systemprompt: invalid prompt variable name %q (must match %s)",
			name,
			variableNamePattern.String(),
		)
	}
	if provider == nil {
		return fmt.Errorf("systemprompt: prompt variable %q provider is nil", name)
	}
	if err := owner.store.addVariable(name, provider); err != nil {
		return err
	}
	if err := owner.publishChanged(requestContext); err != nil {
		owner.store.removeVariable(name)
		return err
	}
	return nil
}

// RemoveVariable removes one named provider from this exact layer.
func (owner *Registry) RemoveVariable(
	requestContext context.Context,
	name string,
) error {
	if !owner.store.removeVariable(name) {
		return nil
	}
	return owner.publishChanged(requestContext)
}

// Assemble resolves a detached layer snapshot and runs the typed assembly
// Waterfall visible from this concrete root or overlay Plugin.
func (owner *Registry) Assemble(
	requestContext context.Context,
	assemblyContext AssembleContext,
) (PromptAssembly, error) {
	layers := owner.promptLayers()
	assembled, completeSection, suppressed, err := assembleLayers(
		requestContext,
		assemblyContext,
		layers,
		owner.configuredToolOrder(),
	)
	if err != nil {
		return PromptAssembly{}, err
	}
	request := AssembleRequest{
		Context: assemblyContext,
	}
	transformed, err := plugin.Run(
		requestContext,
		owner,
		request,
		&assemblyAction{
			assembled: assembled,
		},
	)
	if err != nil {
		return PromptAssembly{}, err
	}
	if err := validateAssembly(transformed); err != nil {
		return PromptAssembly{}, err
	}
	transformed = cloneAssembly(transformed)
	if completeSection != nil {
		transformed.Sections = []AssembledSection{
			*completeSection,
		}
	}
	if suppressed {
		transformed.Contexts = []AssembledContext{}
	}
	return transformed, nil
}

func (owner *Registry) promptLayers() []promptLayerSnapshot {
	layers := make([]promptLayerSnapshot, 0, 2)
	if owner.parent != nil {
		layers = append(layers, owner.parent.promptLayers()...)
	}
	return append(layers, owner.store.capture())
}

func (owner *Registry) configuredToolOrder() []string {
	if owner.parent != nil {
		return owner.parent.configuredToolOrder()
	}
	return slices.Clone(owner.settings.toolOrder)
}

func (owner *Registry) publishChanged(requestContext context.Context) error {
	return plugin.Publish(requestContext, owner, Changed{})
}
