package systemprompt

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"sync"
)

var variableNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

const disabledRuntimeContextSuppressor = "configuration:runtime-context-disabled"

type layerSource interface {
	Assembler
	promptLayers() []promptLayerSnapshot
	configuredToolOrder() []string
}

// PromptLayerFactory creates one plain child PromptLayer. The returned layer is
// not a Plugin and inherits this provider's root prompt view.
type PromptLayerFactory interface {
	NewLayer() *PromptLayer
}

type layerEffects interface {
	ResolveAssembly(
		context.Context,
		AssembleRequest,
		PromptAssembly,
	) (PromptAssembly, error)
	PublishChanged(context.Context) error
}

// PromptLayer owns one System Prompt layer independently from Plugin lifecycle.
// Its parent and effect adapter point toward provider capabilities only; the
// PromptLayer never owns or references the Plugin that created it.
type PromptLayer struct {
	root            bool
	settings        ValidatedConfig
	store           *promptStore
	parent          layerSource
	effects         layerEffects
	middlewareMutex sync.RWMutex
	middleware      []*AssemblyMiddlewareHandle
}

// AddSection adds one named section to this exact layer.
func (owner *PromptLayer) AddSection(
	requestContext context.Context,
	definition PromptSection,
) (*PromptHandle, error) {
	if err := validatePromptMutationContext(requestContext); err != nil {
		return nil, err
	}
	if err := validateEntryName("prompt section", definition.Name); err != nil {
		return nil, err
	}
	if definition.Text == nil {
		return nil, fmt.Errorf(
			"systemprompt: prompt section %q text provider is nil",
			definition.Name,
		)
	}
	if math.IsNaN(definition.Order) || math.IsInf(definition.Order, 0) {
		return nil, fmt.Errorf(
			"systemprompt: prompt section %q order must be a finite number",
			definition.Name,
		)
	}
	token, err := owner.store.addSection(definition)
	if err != nil {
		return nil, err
	}
	if err := owner.publishChanged(requestContext); err != nil {
		owner.store.removeSection(definition.Name, token)
		return nil, err
	}
	return owner.newPromptHandle(
		promptSectionEntry,
		definition.Name,
		token,
	), nil
}

// AddContext adds one named runtime-context provider to this exact layer.
func (owner *PromptLayer) AddContext(
	requestContext context.Context,
	definition PromptContext,
) (*PromptHandle, error) {
	if err := validatePromptMutationContext(requestContext); err != nil {
		return nil, err
	}
	if err := validateEntryName("prompt context", definition.Name); err != nil {
		return nil, err
	}
	if definition.Text == nil {
		return nil, fmt.Errorf(
			"systemprompt: prompt context %q text provider is nil",
			definition.Name,
		)
	}
	if math.IsNaN(definition.Order) || math.IsInf(definition.Order, 0) {
		return nil, fmt.Errorf(
			"systemprompt: prompt context %q order must be a finite number",
			definition.Name,
		)
	}
	token, err := owner.store.addContext(definition)
	if err != nil {
		return nil, err
	}
	if err := owner.publishChanged(requestContext); err != nil {
		owner.store.removeContext(definition.Name, token)
		return nil, err
	}
	return owner.newPromptHandle(
		promptContextEntry,
		definition.Name,
		token,
	), nil
}

// AddRuntimeContextSuppressor disables all runtime context for this layer and
// its descendants until the same named suppressor is removed.
func (owner *PromptLayer) AddRuntimeContextSuppressor(
	requestContext context.Context,
	name string,
) (*PromptHandle, error) {
	if err := validatePromptMutationContext(requestContext); err != nil {
		return nil, err
	}
	if err := validateEntryName("runtime-context suppressor", name); err != nil {
		return nil, err
	}
	token, err := owner.store.addSuppressor(name)
	if err != nil {
		return nil, err
	}
	if err := owner.publishChanged(requestContext); err != nil {
		owner.store.removeSuppressor(name, token)
		return nil, err
	}
	return owner.newPromptHandle(
		promptSuppressorEntry,
		name,
		token,
	), nil
}

// AddToolProvider adds one named assembly-time tool-schema provider.
func (owner *PromptLayer) AddToolProvider(
	requestContext context.Context,
	name string,
	provider ToolProvider,
) (*PromptHandle, error) {
	if err := validatePromptMutationContext(requestContext); err != nil {
		return nil, err
	}
	if err := validateEntryName("tool provider", name); err != nil {
		return nil, err
	}
	if provider == nil {
		return nil, fmt.Errorf("systemprompt: tool provider %q is nil", name)
	}
	token, err := owner.store.addToolProvider(name, provider)
	if err != nil {
		return nil, err
	}
	if err = owner.publishChanged(requestContext); err != nil {
		owner.store.removeToolProvider(name, token)
		return nil, err
	}
	return owner.newPromptHandle(
		promptToolProviderEntry,
		name,
		token,
	), nil
}

// AddVariable adds one named variable provider to this exact layer.
func (owner *PromptLayer) AddVariable(
	requestContext context.Context,
	name string,
	provider VariableProvider,
) (*PromptHandle, error) {
	if err := validatePromptMutationContext(requestContext); err != nil {
		return nil, err
	}
	if !variableNamePattern.MatchString(name) {
		return nil, fmt.Errorf(
			"systemprompt: invalid prompt variable name %q (must match %s)",
			name,
			variableNamePattern.String(),
		)
	}
	if provider == nil {
		return nil, fmt.Errorf("systemprompt: prompt variable %q provider is nil", name)
	}
	token, err := owner.store.addVariable(name, provider)
	if err != nil {
		return nil, err
	}
	if err := owner.publishChanged(requestContext); err != nil {
		owner.store.removeVariable(name, token)
		return nil, err
	}
	return owner.newPromptHandle(
		promptVariableEntry,
		name,
		token,
	), nil
}

func (owner *PromptLayer) newPromptHandle(
	kind promptEntryKind,
	name string,
	token *promptEntryToken,
) *PromptHandle {
	return &PromptHandle{
		owner: owner,
		kind:  kind,
		name:  name,
		token: token,
	}
}

func (owner *PromptLayer) unregisterPrompt(
	requestContext context.Context,
	kind promptEntryKind,
	name string,
	token *promptEntryToken,
) (bool, error) {
	if err := validatePromptMutationContext(requestContext); err != nil {
		return false, err
	}
	removed := false
	switch kind {
	case promptSectionEntry:
		removed = owner.store.removeSection(name, token)
	case promptContextEntry:
		removed = owner.store.removeContext(name, token)
	case promptVariableEntry:
		removed = owner.store.removeVariable(name, token)
	case promptToolProviderEntry:
		removed = owner.store.removeToolProvider(name, token)
	case promptSuppressorEntry:
		removed = owner.store.removeSuppressor(name, token)
	default:
		return true, errors.New("systemprompt: Prompt handle has an invalid entry kind")
	}
	if !removed {
		return true, nil
	}
	return true, owner.publishChanged(requestContext)
}

func validatePromptMutationContext(requestContext context.Context) error {
	if requestContext == nil {
		return errors.New("systemprompt: mutation Context is nil")
	}
	return requestContext.Err()
}

// Assemble resolves a detached layer snapshot and runs the typed assembly
// Waterfall visible from this concrete root or overlay Plugin.
func (owner *PromptLayer) Assemble(
	requestContext context.Context,
	assemblyContext AssembleContext,
) (PromptAssembly, error) {
	resolver := assemblyResolver{
		requestContext:  requestContext,
		assemblyContext: assemblyContext,
		layers:          owner.promptLayers(),
		toolOrder:       owner.configuredToolOrder(),
	}
	prepared, err := resolver.resolve()
	if err != nil {
		return PromptAssembly{}, err
	}
	request := AssembleRequest{
		Context: assemblyContext,
	}
	terminal := AssemblyAction(assemblyEffectsAction{
		effects:   owner.effects,
		candidate: prepared.candidate,
	})
	owner.middlewareMutex.RLock()
	middleware := append([]*AssemblyMiddlewareHandle(nil), owner.middleware...)
	owner.middlewareMutex.RUnlock()
	for index := len(middleware) - 1; index >= 0; index-- {
		current, active := middleware[index].middlewareValue()
		if !active {
			continue
		}
		terminal = assemblyMiddlewareAction{
			middleware: current,
			next:       terminal,
		}
	}
	transformed, err := terminal.Execute(requestContext, request)
	if err != nil {
		return PromptAssembly{}, err
	}
	return prepared.finalize(transformed)
}

func (owner *PromptLayer) promptLayers() []promptLayerSnapshot {
	layers := make([]promptLayerSnapshot, 0, 2)
	if owner.parent != nil {
		layers = append(layers, owner.parent.promptLayers()...)
	}
	return append(layers, owner.store.capture())
}

func (owner *PromptLayer) configuredToolOrder() []string {
	if owner.parent != nil {
		return owner.parent.configuredToolOrder()
	}
	return slices.Clone(owner.settings.toolOrder)
}

func (owner *PromptLayer) publishChanged(requestContext context.Context) error {
	if owner == nil || owner.effects == nil {
		return errors.New("systemprompt: PromptLayer effects are unavailable")
	}
	return owner.effects.PublishChanged(requestContext)
}

func (owner *PromptLayer) close() {
	if owner == nil {
		return
	}
	owner.store.clear()
	owner.parent = nil
	owner.middlewareMutex.Lock()
	middleware := owner.middleware
	owner.middleware = nil
	owner.middlewareMutex.Unlock()
	for _, handle := range middleware {
		_ = handle.Close(context.Background())
	}
}

// Close releases this plain child layer. It is safe to call repeatedly.
func (owner *PromptLayer) Close(context.Context) error {
	owner.close()
	return nil
}
