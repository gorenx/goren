// Package promptactor owns the GoAkt boundary for prompt registration state.
package promptactor

import (
	"context"
	"github.com/gorenx/goren/systemprompt"
)

// Initialize supplies pure business state from the composition root.
type Initialize struct {
	Layer *systemprompt.PromptLayer
}

// ApplyBaseline replaces only configuration-owned shared prompt definitions.
type ApplyBaseline struct {
	Context context.Context
	Config  systemprompt.Config
}

// Prepare captures registration membership without invoking user strategies.
type Prepare struct{}

// Prepared is the value used to evaluate one model request outside the mailbox.
type Prepared struct {
	// Config is captured together with Snapshot from the same root baseline.
	Config   systemprompt.Config
	Version  uint64
	Snapshot systemprompt.Snapshot
	Err      error
}

// Register is the closed set of prompt registration commands.
// Named commands preserve their own parameter types across the module boundary.
type Register interface {
	register(context.Context, *systemprompt.PromptLayer) (systemprompt.RegistrationID, error)
}

// Section registers one ordered system instruction.
type Section struct {
	Definition systemprompt.PromptSection
}

func (entry Section) register(ctx context.Context, owner *systemprompt.PromptLayer) (systemprompt.RegistrationID, error) {
	return owner.AddSection(ctx, entry.Definition)
}

// Context registers one dynamic runtime-context entry.
type Context struct {
	Definition systemprompt.PromptContext
}

func (entry Context) register(ctx context.Context, owner *systemprompt.PromptLayer) (systemprompt.RegistrationID, error) {
	return owner.AddContext(ctx, entry.Definition)
}

// Variable registers one named interpolation provider.
type Variable struct {
	Name     string
	Provider systemprompt.VariableProvider
}

func (entry Variable) register(ctx context.Context, owner *systemprompt.PromptLayer) (systemprompt.RegistrationID, error) {
	return owner.AddVariable(ctx, entry.Name, entry.Provider)
}

// Suppressor disables runtime context in the effective prompt.
type Suppressor struct {
	Name string
}

func (entry Suppressor) register(ctx context.Context, owner *systemprompt.PromptLayer) (systemprompt.RegistrationID, error) {
	return owner.AddRuntimeContextSuppressor(ctx, entry.Name)
}

// Registered exposes an opaque registration identity, never a business pointer.
type Registered struct {
	ID      systemprompt.RegistrationID
	Version uint64
	Err     error
}

// Remove releases exactly the identified registration; repeats are idempotent.
type Remove struct {
	ID systemprompt.RegistrationID
}

// Completion acknowledges a command while preserving its business error.
type Completion struct {
	Removed bool
	Err     error
}
