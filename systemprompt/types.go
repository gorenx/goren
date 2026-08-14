// Package systemprompt owns ordered model-instruction, runtime-context,
// tool-schema, and prompt-variable assembly.
package systemprompt

import (
	"context"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
)

const (
	// ServiceName is the canonical Cordis service name.
	ServiceName = "systemPrompt"
	// AssembleEventName is the canonical scoped assembly waterfall event.
	AssembleEventName = "system-prompt/assemble"
	// ChangeEventName is the canonical unfiltered registry-change event.
	ChangeEventName = "system-prompt/change"
	// PersonaSection is the deployment persona slot that a scoped preset may shadow.
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

// AssembleContext selects the contribution and listener scope for one
// assembly. Cancellation and deadlines travel through context.Context.
type AssembleContext struct {
	Scope plugin.ScopeKey
}

// TextProvider resolves one section or runtime-context contribution for each
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

// TextFunc adapts a function to TextProvider.
type TextFunc func(context.Context, AssembleContext) (string, error)

// ResolveText invokes the function.
func (operation TextFunc) ResolveText(requestContext context.Context, assemblyContext AssembleContext) (string, error) {
	return operation(requestContext, assemblyContext)
}

// PromptSection is one named, ordered system-prompt contribution.
type PromptSection struct {
	Name     string
	Order    float64
	Text     TextProvider
	Complete bool
}

// PromptContext is one named, ordered dynamic runtime-context contribution.
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

// AssembledContext is one runtime-context contribution after resolution.
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
type VariableProvider func(context.Context, AssembleContext) (VariableValue, error)

// ToolProviderResult contains schemas visible in this assembly and the
// pre-restriction name universe used to validate configured tool order.
type ToolProviderResult struct {
	Schemas    []llm.ToolSchema
	KnownNames []string
}

// ToolProvider resolves model-visible tool schemas for each assembly.
type ToolProvider func(context.Context, AssembleContext) (ToolProviderResult, error)

// PromptAssembly is the complete, still-unrendered model-input snapshot.
type PromptAssembly struct {
	Sections  []AssembledSection
	Contexts  []AssembledContext
	Tools     []llm.ToolSchema
	Variables map[string]VariableValue
}

// AssembleNext delegates to the next assembly listener.
type AssembleNext func(context.Context) (PromptAssembly, error)

// AssembleHandler may transform or short-circuit one scoped assembly.
type AssembleHandler func(context.Context, *PromptAssembly, AssembleContext, AssembleNext) (PromptAssembly, error)

// ChangeHandler observes any registration or disposal change. Change events
// are unfiltered because a global registration can affect every scope.
type ChangeHandler func(context.Context) error

// SystemPrompt is the provider-owned registry and assembly capability.
type SystemPrompt interface {
	Section(context.Context, *plugin.Scope, PromptSection) (plugin.Disposer, error)
	Context(context.Context, *plugin.Scope, PromptContext) (plugin.Disposer, error)
	SuppressRuntimeContext(context.Context, *plugin.Scope) (plugin.Disposer, error)
	Tools(context.Context, *plugin.Scope, ToolProvider) (plugin.Disposer, error)
	Variable(context.Context, *plugin.Scope, string, VariableProvider) (plugin.Disposer, error)
	Assemble(context.Context, AssembleContext) (PromptAssembly, error)
}

// Service is the canonical System Prompt service definition.
var Service = plugin.DefineService[SystemPrompt](ServiceName)

type assemblePayload struct {
	assembled       *PromptAssembly
	assemblyContext AssembleContext
}

var (
	assembleEvent = plugin.DefineEvent[assemblePayload, PromptAssembly](AssembleEventName, plugin.ModeWaterfall)
	changeEvent   = plugin.DefineEvent[struct{}, struct{}](ChangeEventName, plugin.ModeEmit)
)

// OnAssemble registers one scope-owned assembly waterfall listener.
func OnAssemble(pluginScope *plugin.Scope, callback AssembleHandler) (plugin.Disposer, error) {
	if callback == nil {
		return nil, errNilAssembleHandler
	}
	return plugin.OnWaterfall(pluginScope, assembleEvent,
		func(requestContext context.Context, payload assemblePayload, downstream plugin.Next[assemblePayload, PromptAssembly]) (PromptAssembly, error) {
			return callback(requestContext, payload.assembled, payload.assemblyContext,
				func(chainContext context.Context) (PromptAssembly, error) {
					return downstream(chainContext, payload)
				})
		})
}

// OnChange registers one scope-owned unfiltered registry-change listener.
func OnChange(pluginScope *plugin.Scope, callback ChangeHandler) (plugin.Disposer, error) {
	if callback == nil {
		return nil, errNilChangeHandler
	}
	return plugin.OnNotify(pluginScope, changeEvent, func(requestContext context.Context, _ struct{}) error {
		return callback(requestContext)
	})
}
