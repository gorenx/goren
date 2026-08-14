package systemprompt

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sync/atomic"

	"github.com/gorenx/goren/plugin"
)

var (
	variableNamePattern   = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	errNilAssembleHandler = errors.New("systemprompt: assemble listener is nil")
	errNilChangeHandler   = errors.New("systemprompt: change listener is nil")
)

// promptRegistry is the SystemPrompt service facade. It validates service
// calls, binds contributions to plugin lifecycles, and delegates storage and
// assembly to their dedicated components.
type promptRegistry struct {
	sourceScope *plugin.Scope
	store       *promptStore
	assembler   promptAssembler
}

// New creates one System Prompt registry from prevalidated configuration and
// installs its built-ins as effects owned by sourceScope.
func New(requestContext context.Context, sourceScope *plugin.Scope, settings ValidatedConfig) (SystemPrompt, error) {
	if sourceScope == nil {
		return nil, errors.New("systemprompt: source scope is nil")
	}
	storage := newPromptStore()
	owner := &promptRegistry{
		sourceScope: sourceScope,
		store:       storage,
		assembler:   newPromptAssembler(sourceScope, storage, settings.toolOrder),
	}
	if settings.includeHarnessIdentity {
		if _, err := owner.Section(requestContext, sourceScope, PromptSection{
			Name: harnessIdentityName, Order: harnessIdentityOrder, Text: StaticText(harnessIdentityText),
		}); err != nil {
			return nil, err
		}
	}
	if _, err := owner.Section(requestContext, sourceScope, PromptSection{
		Name: PersonaSection, Order: PersonaOrder, Text: StaticText(settings.persona),
	}); err != nil {
		return nil, err
	}
	if !settings.includeRuntimeContext {
		if _, err := owner.SuppressRuntimeContext(requestContext, sourceScope); err != nil {
			return nil, err
		}
	}
	return owner, nil
}

// Section registers a named section in ownerScope's exact contribution layer.
func (owner *promptRegistry) Section(requestContext context.Context, ownerScope *plugin.Scope, definition PromptSection) (plugin.Disposer, error) {
	if ownerScope == nil {
		return nil, errors.New("systemprompt: section owner scope is nil")
	}
	if definition.Text == nil {
		return nil, fmt.Errorf("systemprompt: prompt section %q text provider is nil", definition.Name)
	}
	if math.IsNaN(definition.Order) || math.IsInf(definition.Order, 0) {
		return nil, fmt.Errorf("systemprompt: prompt section %q order must be a finite number", definition.Name)
	}
	undo, err := owner.store.addSection(ownerScope.Target(), definition)
	if err != nil {
		return nil, err
	}
	return owner.ownMutation(requestContext, ownerScope, "systemPrompt.section()", undo)
}

// Context registers named dynamic runtime context in ownerScope's exact layer.
func (owner *promptRegistry) Context(requestContext context.Context, ownerScope *plugin.Scope, definition PromptContext) (plugin.Disposer, error) {
	if ownerScope == nil {
		return nil, errors.New("systemprompt: context owner scope is nil")
	}
	if definition.Text == nil {
		return nil, fmt.Errorf("systemprompt: prompt context %q text provider is nil", definition.Name)
	}
	if math.IsNaN(definition.Order) || math.IsInf(definition.Order, 0) {
		return nil, fmt.Errorf("systemprompt: prompt context %q order must be a finite number", definition.Name)
	}
	undo, err := owner.store.addContext(ownerScope.Target(), definition)
	if err != nil {
		return nil, err
	}
	return owner.ownMutation(requestContext, ownerScope, "systemPrompt.context()", undo)
}

// SuppressRuntimeContext hides every registered context in ownerScope's view.
func (owner *promptRegistry) SuppressRuntimeContext(requestContext context.Context, ownerScope *plugin.Scope) (plugin.Disposer, error) {
	if ownerScope == nil {
		return nil, errors.New("systemprompt: suppressor owner scope is nil")
	}
	undo := owner.store.addSuppressor(ownerScope.Target())
	return owner.ownMutation(requestContext, ownerScope, "systemPrompt.suppressRuntimeContext()", undo)
}

// Tools registers one assembly-time tool-schema provider.
func (owner *promptRegistry) Tools(requestContext context.Context, ownerScope *plugin.Scope, callback ToolProvider) (plugin.Disposer, error) {
	if ownerScope == nil {
		return nil, errors.New("systemprompt: tool provider owner scope is nil")
	}
	if callback == nil {
		return nil, errors.New("systemprompt: tool provider is nil")
	}
	undo := owner.store.addToolProvider(ownerScope.Target(), callback)
	return owner.ownMutation(requestContext, ownerScope, "systemPrompt.tools()", undo)
}

// Variable registers one prompt variable in ownerScope's exact layer.
func (owner *promptRegistry) Variable(requestContext context.Context, ownerScope *plugin.Scope, name string, callback VariableProvider) (plugin.Disposer, error) {
	if ownerScope == nil {
		return nil, errors.New("systemprompt: variable owner scope is nil")
	}
	if !variableNamePattern.MatchString(name) {
		return nil, fmt.Errorf("systemprompt: invalid prompt variable name %q (must match %s)", name, variableNamePattern.String())
	}
	if callback == nil {
		return nil, fmt.Errorf("systemprompt: prompt variable %q provider is nil", name)
	}
	undo, err := owner.store.addVariable(ownerScope.Target(), name, callback)
	if err != nil {
		return nil, err
	}
	return owner.ownMutation(requestContext, ownerScope, "systemPrompt.variable()", undo)
}

// Assemble delegates immutable snapshot resolution to the assembly component.
func (owner *promptRegistry) Assemble(requestContext context.Context, assemblyContext AssembleContext) (PromptAssembly, error) {
	return owner.assembler.assemble(requestContext, assemblyContext)
}

func (owner *promptRegistry) ownMutation(requestContext context.Context, ownerScope *plugin.Scope, label string, undo func()) (plugin.Disposer, error) {
	var initializing atomic.Bool
	initializing.Store(true)
	release, err := plugin.Own(ownerScope, label, func(closeContext context.Context) error {
		undo()
		if initializing.Load() {
			return nil
		}
		return plugin.EmitFrom(closeContext, owner.sourceScope, changeEvent, struct{}{})
	})
	if err != nil {
		undo()
		return nil, err
	}
	if err := plugin.EmitFrom(requestContext, owner.sourceScope, changeEvent, struct{}{}); err != nil {
		return nil, errors.Join(err, release(requestContext))
	}
	initializing.Store(false)
	return release, nil
}
