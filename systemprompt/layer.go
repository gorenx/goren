package systemprompt

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
)

var variableNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

const disabledRuntimeContextSuppressor = "configuration:runtime-context-disabled"

// PromptLayer owns one System Prompt layer independently from Plugin lifecycle.
// Inherited definitions are value snapshots; it never retains a parent or Plugin.
// Its owner must serialize registration, capture, base updates, and close.
type PromptLayer struct {
	settings ValidatedConfig
	store    *promptStore
	base     Snapshot
}

// AddSection adds one named section to this exact layer.
func (owner *PromptLayer) AddSection(
	ctx context.Context,
	definition PromptSection,
) (RegistrationID, error) {
	if err := validatePromptMutationContext(ctx); err != nil {
		return "", err
	}
	if err := validateEntryName("prompt section", definition.Name); err != nil {
		return "", err
	}
	if definition.Text == nil {
		return "", fmt.Errorf(
			"systemprompt: prompt section %q text provider is nil",
			definition.Name,
		)
	}
	if math.IsNaN(definition.Order) || math.IsInf(definition.Order, 0) {
		return "", fmt.Errorf(
			"systemprompt: prompt section %q order must be a finite number",
			definition.Name,
		)
	}
	return owner.store.addSection(definition)
}

// AddContext adds one named runtime-context provider to this exact layer.
func (owner *PromptLayer) AddContext(
	ctx context.Context,
	definition PromptContext,
) (RegistrationID, error) {
	if err := validatePromptMutationContext(ctx); err != nil {
		return "", err
	}
	if err := validateEntryName("prompt context", definition.Name); err != nil {
		return "", err
	}
	if definition.Text == nil {
		return "", fmt.Errorf(
			"systemprompt: prompt context %q text provider is nil",
			definition.Name,
		)
	}
	if math.IsNaN(definition.Order) || math.IsInf(definition.Order, 0) {
		return "", fmt.Errorf(
			"systemprompt: prompt context %q order must be a finite number",
			definition.Name,
		)
	}
	return owner.store.addContext(definition)
}

// AddRuntimeContextSuppressor disables all runtime context for this layer and
// its descendants until the same named suppressor is removed.
func (owner *PromptLayer) AddRuntimeContextSuppressor(
	ctx context.Context,
	name string,
) (RegistrationID, error) {
	if err := validatePromptMutationContext(ctx); err != nil {
		return "", err
	}
	if err := validateEntryName("runtime-context suppressor", name); err != nil {
		return "", err
	}
	return owner.store.addSuppressor(name)
}

// AddVariable adds one named variable provider to this exact layer.
func (owner *PromptLayer) AddVariable(
	ctx context.Context,
	name string,
	provider VariableProvider,
) (RegistrationID, error) {
	if err := validatePromptMutationContext(ctx); err != nil {
		return "", err
	}
	if !variableNamePattern.MatchString(name) {
		return "", fmt.Errorf(
			"systemprompt: invalid prompt variable name %q (must match %s)",
			name,
			variableNamePattern.String(),
		)
	}
	if provider == nil {
		return "", fmt.Errorf("systemprompt: prompt variable %q provider is nil", name)
	}
	return owner.store.addVariable(name, provider)
}

// Remove deletes exactly one registration. Unknown or previously removed IDs are no-ops.
func (owner *PromptLayer) Remove(ctx context.Context, identifier RegistrationID) (bool, error) {
	if err := validatePromptMutationContext(ctx); err != nil {
		return false, err
	}
	return owner.store.remove(identifier), nil
}

func validatePromptMutationContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("systemprompt: mutation Context is nil")
	}
	return ctx.Err()
}

// Assemble evaluates one fixed registration snapshot.
func (owner *PromptLayer) Assemble(ctx context.Context, assemblyContext AssembleContext) (PromptAssembly, error) {
	return owner.Capture().Assemble(ctx, assemblyContext)
}

// Capture freezes registration membership for one model request.
func (owner *PromptLayer) Capture() Snapshot {
	layers := append([]promptLayerSnapshot(nil), owner.base.layers...)
	layers = append(layers, owner.store.capture())
	order := slices.Clone(owner.settings.toolOrder)
	if len(owner.base.layers) != 0 {
		order = slices.Clone(owner.base.toolOrder)
	}
	return Snapshot{
		layers:    layers,
		toolOrder: order,
	}
}

// SetBase explicitly replaces inherited definitions with a captured value.
// The caller serializes this operation with other registration changes.
func (owner *PromptLayer) SetBase(inherited Snapshot) {
	owner.base = inherited
}

func (owner *PromptLayer) close() {
	if owner == nil {
		return
	}
	owner.store.clear()
	owner.base = Snapshot{}

}

// Close releases this plain child layer. It is safe to call repeatedly.
func (owner *PromptLayer) Close(context.Context) error {
	owner.close()
	return nil
}
