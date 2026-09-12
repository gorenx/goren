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

// Config is the owner-defined, serializable System Prompt configuration.
// It controls built-in sections, runtime-context suppression and tool order.
// Custom PromptSection and PromptContext definitions use PromptRegistry;
// their TextProvider implementations are Go capabilities, not config values.
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

// AssembleContext is the per-call input passed to text and variable providers.
// It currently contains the consumed tool-definition view, supplied by Runtime;
// it is neither a registered PromptContext nor an assembled context entry.
// The separate context.Context argument controls cancellation and deadlines.
// A Snapshot selects definitions; mutable Session owners and user-configurable
// tool registrations are not exposed through this input.
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

// PromptSection defines an instruction entry registered with AddSection.
// Text may be StaticText or a provider evaluated on each assembly. Definitions
// are merged by Name across layers, with a child shadowing its inherited entry,
// then sorted by ascending Order. Section and context names are independent.
// Duplicate names within the same layer are rejected.
// Assembly produces AssembledSection values; RenderPrompt renders their text.
type PromptSection struct {
	Name  string
	Order float64
	Text  TextProvider
	// Complete makes this entry the only section in a successful assembly.
	// Multiple active complete entries fail. Other section providers still run
	// and may fail; contexts, variables and tools are not replaced.
	Complete bool
}

// PromptContext defines a runtime fact entry registered with AddContext,
// for example a tool summary. Like PromptSection, it accepts static or computed
// text, uses child-by-name overrides and is sorted by ascending Order.
// Its output goes to PromptAssembly.Contexts and RenderContextSections, not
// RenderPrompt. A runtime-context suppressor skips these providers entirely.
// AgentLoop owns committing the rendered context to the Session conversation.
type PromptContext struct {
	Name  string
	Order float64
	Text  TextProvider
}

// AssembledSection is the output of resolving a PromptSection's TextProvider.
// Order is represented by its position in PromptAssembly.Sections; the provider
// and Complete flag are no longer needed. Text may still contain {{name}}
// references: RenderPrompt interpolates them using the assembly's variables.
// This is an assembly result, not a definition to register or user settings.
type AssembledSection struct {
	Name string `json:"name"`
	Text string `json:"text"`
}

// AssembledContext is the output of resolving a PromptContext's TextProvider.
// Order is represented by its position in PromptAssembly.Contexts. Text may
// still contain {{name}} references, resolved later by RenderContextSections.
// It is neither the AssembleContext input nor an already-committed Session
// message; the caller owns rendering and committing the resulting content.
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

// PromptAssembly contains resolved prompt sections, runtime context, tool schemas,
// and variables. It contains no conversation history or complete LLM request;
// AgentLoop derives messages from the committed Session separately.
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
