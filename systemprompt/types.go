// Package systemprompt owns ordered model-instruction, runtime-context,
// tool-schema, and prompt-variable assembly.
package systemprompt

import (
	"context"

	"github.com/gorenx/goren/llm"
)

const (
	// PersonaSection is the deployment persona slot that a child layer may shadow.
	PersonaSection = "deployment:persona"
	// PersonaOrder is the canonical order of the deployment persona slot.
	PersonaOrder = 0
	// ToolOrderRest marks where unlisted tool schemas are inserted.
	ToolOrderRest = "<unlisted-tools>"

	harnessIdentityName  = "harness:identity"
	harnessIdentityText  = "You are an AI agent powered by DeepSeek Harness."
	harnessIdentityOrder = -100
)

// Config is the owner-defined typed System Prompt configuration.
type Config struct {
	IncludeHarnessIdentity *bool    `json:"includeHarnessIdentity,omitempty"`
	IncludeRuntimeContext  *bool    `json:"includeRuntimeContext,omitempty"`
	Persona                string   `json:"persona,omitempty"`
	ToolOrder              []string `json:"toolOrder,omitempty"`
}

// ValidatedConfig is the immutable result of owner-defined configuration
// validation. Composition validates it before constructing business state.
type ValidatedConfig struct {
	includeHarnessIdentity bool
	includeRuntimeContext  bool
	persona                string
	toolOrder              []string
}

// AssembleContext contains business data available while one prompt is
// assembled. A fixed Snapshot selects definitions; mutable Session owners are
// not exposed to text or variable providers.
type AssembleContext struct {
	Tools ToolDefinitions
}

// TextProvider resolves one section or runtime-context entry for each
// assembly request.
type TextProvider interface {
	ResolveText(context.Context, AssembleContext) (string, error)
}

// StaticText adapts literal text to TextProvider.
type StaticText string

// ResolveText returns the retained literal.
func (content StaticText) ResolveText(context.Context, AssembleContext) (string, error) {
	return string(content), nil
}

// PromptSection is one named, ordered system-prompt entry.
type PromptSection struct {
	Name     string
	Order    float64
	Text     TextProvider
	Complete bool
}

// PromptContext is one named, ordered dynamic runtime-context entry.
type PromptContext struct {
	Name  string
	Order float64
	Text  TextProvider
}

// AssembledSection is one prompt section after its text provider resolves.
type AssembledSection struct {
	Name string `json:"name"`
	Text string `json:"text"`
}

// AssembledContext is one runtime-context entry after resolution.
type AssembledContext struct {
	Name string `json:"name"`
	Text string `json:"text"`
}

// VariableValue retains the distinction between an empty string and an
// undefined value. Rendering a referenced undefined value fails.
type VariableValue struct {
	Value   string
	Defined bool
}

// ResolveVariable makes a fixed value available through the variable provider contract.
func (value VariableValue) ResolveVariable(context.Context, AssembleContext) (VariableValue, error) {
	return value, nil
}

// VariableProvider resolves one prompt variable for each assembly.
type VariableProvider interface {
	ResolveVariable(context.Context, AssembleContext) (VariableValue, error)
}

// ToolDefinitions contains schemas visible in this assembly and the
// pre-restriction name universe used to validate configured tool order.
type ToolDefinitions struct {
	Schemas    []llm.ToolSchema
	KnownNames []string
}

// PromptAssembly is the complete, still-unrendered model-input snapshot.
type PromptAssembly struct {
	Sections  []AssembledSection
	Contexts  []AssembledContext
	Tools     []llm.ToolSchema
	Variables map[string]VariableValue
}

// Assembler is the read-only System Prompt capability consumed by Agent Loop.
type Assembler interface {
	Assemble(context.Context, AssembleContext) (PromptAssembly, error)
}

// PromptRegistry changes definitions in one registration owner.
type PromptRegistry interface {
	Remove(context.Context, RegistrationID) (bool, error)
	AddSection(context.Context, PromptSection) (RegistrationID, error)
	AddContext(context.Context, PromptContext) (RegistrationID, error)
	AddRuntimeContextSuppressor(context.Context, string) (RegistrationID, error)
	AddVariable(context.Context, string, VariableProvider) (RegistrationID, error)
}

// SystemPrompt is the complete provider contract implemented by Registry.
// Consumers should depend on Assembler or PromptRegistry rather than this
// composite interface.
type SystemPrompt interface {
	Assembler
	PromptRegistry
}

// RegistrationID identifies one registration across removals and owner lifetimes.
// It carries no owner reference and is never reused for a same-name replacement.
type RegistrationID string
