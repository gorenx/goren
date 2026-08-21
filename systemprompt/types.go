// Package systemprompt owns ordered model-instruction, runtime-context,
// tool-schema, and prompt-variable assembly.
package systemprompt

import (
	"context"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
)

const (
	// PluginName is the canonical Harness System Prompt Plugin name.
	PluginName = "@deepseek-ai/dsh-system-prompt"
	// OverlayPluginName identifies a child System Prompt layer.
	OverlayPluginName = "@deepseek-ai/dsh-system-prompt/overlay"
	// AssembleEventName retains the canonical source name of the assembly
	// waterfall for compatibility evidence and diagnostics.
	AssembleEventName = "system-prompt/assemble"
	// ChangeEventName is the canonical registry-change Event name.
	ChangeEventName = "system-prompt/change"
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
// validation. Factory boundaries construct it before creating a Plugin.
type ValidatedConfig struct {
	includeHarnessIdentity bool
	includeRuntimeContext  bool
	persona                string
	toolOrder              []string
}

// AssembleContext contains business data available while one prompt is
// assembled. Runtime Scope selection is owned by the concrete SystemPrompt
// Plugin instance resolved for the caller and is deliberately absent here.
type AssembleContext struct {
	Session *session.Session
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

// TextFunc adapts a stateless function to TextProvider.
type TextFunc func(context.Context, AssembleContext) (string, error)

// ResolveText invokes the adapted function.
func (operation TextFunc) ResolveText(
	requestContext context.Context,
	assemblyContext AssembleContext,
) (string, error) {
	return operation(requestContext, assemblyContext)
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

// VariableProvider resolves one prompt variable for each assembly.
type VariableProvider interface {
	ResolveVariable(context.Context, AssembleContext) (VariableValue, error)
}

// VariableProviderFunc adapts a stateless function to VariableProvider.
type VariableProviderFunc func(context.Context, AssembleContext) (VariableValue, error)

// ResolveVariable invokes the adapted function.
func (operation VariableProviderFunc) ResolveVariable(
	requestContext context.Context,
	assemblyContext AssembleContext,
) (VariableValue, error) {
	return operation(requestContext, assemblyContext)
}

// ToolProviderResult contains schemas visible in this assembly and the
// pre-restriction name universe used to validate configured tool order.
type ToolProviderResult struct {
	Schemas    []llm.ToolSchema
	KnownNames []string
}

// ToolProvider resolves model-visible tool schemas for each assembly.
type ToolProvider interface {
	ResolveTools(context.Context, AssembleContext) (ToolProviderResult, error)
}

// ToolProviderFunc adapts a stateless function to ToolProvider.
type ToolProviderFunc func(context.Context, AssembleContext) (ToolProviderResult, error)

// ResolveTools invokes the adapted function.
func (operation ToolProviderFunc) ResolveTools(
	requestContext context.Context,
	assemblyContext AssembleContext,
) (ToolProviderResult, error) {
	return operation(requestContext, assemblyContext)
}

// AssembleRequest is the typed input of the System Prompt assembly waterfall.
type AssembleRequest struct {
	plugin.WaterfallInputBase
	Context AssembleContext
}

// PromptAssembly is the complete, still-unrendered model-input snapshot and
// the typed output of the System Prompt assembly waterfall.
type PromptAssembly struct {
	plugin.WaterfallOutputBase
	Sections  []AssembledSection
	Contexts  []AssembledContext
	Tools     []llm.ToolSchema
	Variables map[string]VariableValue
}

// Changed reports that one System Prompt layer changed after a successful
// add or removal. It is unfiltered at the domain level; Runtime Scope routing
// still follows the publishing layer's ancestry.
type Changed struct{}

// EventName returns the canonical Harness Event name.
func (Changed) EventName() string {
	return ChangeEventName
}

// EventDelivery preserves synchronous failure propagation so a failed add can
// be rolled back before it becomes observable.
func (Changed) EventDelivery() plugin.DeliveryPolicy {
	return plugin.DeliveryOrdered
}

// Assembler is the read-only System Prompt capability consumed by Agent Loop.
type Assembler interface {
	plugin.Service
	Assemble(context.Context, AssembleContext) (PromptAssembly, error)
}

// PromptRegistry is the mutation capability consumed by Plugins that own
// prompt sections, context, variables, or tool presentation. It changes only
// the exact Registry layer resolved for that Plugin.
type PromptRegistry interface {
	plugin.Service
	AddSection(context.Context, PromptSection) (*PromptHandle, error)
	AddContext(context.Context, PromptContext) (*PromptHandle, error)
	AddRuntimeContextSuppressor(context.Context, string) (*PromptHandle, error)
	AddToolProvider(context.Context, string, ToolProvider) (*PromptHandle, error)
	AddVariable(context.Context, string, VariableProvider) (*PromptHandle, error)
}

// SystemPrompt is the complete provider contract implemented by Registry.
// Consumers should depend on Assembler or PromptRegistry rather than this
// composite interface.
type SystemPrompt interface {
	Assembler
	PromptRegistry
}
