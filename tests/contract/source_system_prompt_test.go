//go:build contract

package contract_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/systemprompt"
)

type promptContractPlugin struct {
	settings systemprompt.Config
	ready    func(systemprompt.SystemPrompt, *plugin.Scope) error
}

func (instance promptContractPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{Name: "system-prompt-contract", Provides: []plugin.ServiceRef{systemprompt.Service.Ref()}}
}

func (instance promptContractPlugin) Apply(requestContext context.Context, pluginScope *plugin.Scope) error {
	validatedSettings, err := systemprompt.ValidateConfig(instance.settings)
	if err != nil {
		return err
	}
	promptService, err := systemprompt.New(requestContext, pluginScope, validatedSettings)
	if err != nil {
		return err
	}
	if _, err := plugin.Provide(pluginScope, systemprompt.Service, promptService); err != nil {
		return err
	}
	return instance.ready(promptService, pluginScope)
}

type promptObservation struct {
	Sections        []systemprompt.AssembledSection `json:"sections"`
	Contexts        []systemprompt.AssembledContext `json:"contexts"`
	Tools           []llm.ToolSchema                `json:"tools"`
	Variables       map[string]*string              `json:"variables"`
	Prompt          string                          `json:"prompt"`
	ContextSnapshot string                          `json:"contextSnapshot"`
}

type renderObservation struct {
	OK    bool   `json:"ok"`
	Value string `json:"value,omitempty"`
}

type systemPromptContractObservation struct {
	Global                  promptObservation            `json:"global"`
	Scoped                  promptObservation            `json:"scoped"`
	AfterDispose            promptObservation            `json:"afterDispose"`
	Complete                promptObservation            `json:"complete"`
	Suppressed              promptObservation            `json:"suppressed"`
	SuppressedProviderCalls int                          `json:"suppressedProviderCalls"`
	Rendering               map[string]renderObservation `json:"rendering"`
}

func TestPinnedSourceSystemPromptMatchesGo(t *testing.T) {
	repositoryRoot, sourceRoot := contractPaths(t)
	commandContext, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sourceOutput, err := runTypeScript(commandContext, sourceRoot,
		filepath.Join(repositoryRoot, "tests", "contract", "typescript", "system-prompt.ts"),
		sourceRoot, filepath.Join(repositoryRoot, "contracts", "deepseek-harness", "manifest.json"),
	)
	if err != nil {
		t.Fatal(err)
	}

	requestContext := context.Background()
	var promptService systemprompt.SystemPrompt
	var providerScope *plugin.Scope
	engine := plugin.NewRuntime()
	if _, err := engine.Load(requestContext, promptContractPlugin{
		settings: systemprompt.Config{
			Persona: "Mode: {{mode}}.", ToolOrder: []string{"todo", systemprompt.ToolOrderRest, "bash"},
		},
		ready: func(available systemprompt.SystemPrompt, pluginScope *plugin.Scope) error {
			promptService = available
			providerScope = pluginScope
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := promptService.Section(requestContext, providerScope, systemprompt.PromptSection{
		Name: "rules", Order: 10, Text: systemprompt.StaticText("Be precise."),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := promptService.Section(requestContext, providerScope, systemprompt.PromptSection{
		Name: "cwd", Order: 20, Text: systemprompt.StaticText("cwd: /tmp"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := promptService.Context(requestContext, providerScope, systemprompt.PromptContext{
		Name: "later", Order: 20, Text: systemprompt.StaticText("context 2"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := promptService.Context(requestContext, providerScope, systemprompt.PromptContext{
		Name: "earlier", Order: 10, Text: systemprompt.StaticText("context {{mode}}"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := promptService.Variable(requestContext, providerScope, "mode",
		func(context.Context, systemprompt.AssembleContext) (systemprompt.VariableValue, error) {
			return systemprompt.VariableValue{Value: "normal", Defined: true}, nil
		}); err != nil {
		t.Fatal(err)
	}
	if _, err := promptService.Tools(requestContext, providerScope,
		func(context.Context, systemprompt.AssembleContext) (systemprompt.ToolProviderResult, error) {
			return systemprompt.ToolProviderResult{Schemas: []llm.ToolSchema{
				{Name: "bash", Description: "shell", Parameters: json.RawMessage(`{"type":"object"}`)},
				{Name: "zeta", Description: "z", Parameters: json.RawMessage(`{}`)},
				{Name: "todo", Description: "tasks", Parameters: json.RawMessage(`{}`)},
				{Name: "alpha", Description: "a", Parameters: json.RawMessage(`{}`)},
			}}, nil
		}); err != nil {
		t.Fatal(err)
	}

	childScope, childRelease, err := providerScope.Child("contract-child")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := promptService.Section(requestContext, childScope, systemprompt.PromptSection{
		Name: systemprompt.PersonaSection, Order: 0, Text: systemprompt.StaticText("Scoped {{mode}}."),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := promptService.Section(requestContext, childScope, systemprompt.PromptSection{
		Name: "child", Order: 15, Text: systemprompt.StaticText("Child section."),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := promptService.Variable(requestContext, childScope, "mode",
		func(context.Context, systemprompt.AssembleContext) (systemprompt.VariableValue, error) {
			return systemprompt.VariableValue{Value: "strict", Defined: true}, nil
		}); err != nil {
		t.Fatal(err)
	}
	if _, err := promptService.Tools(requestContext, childScope,
		func(context.Context, systemprompt.AssembleContext) (systemprompt.ToolProviderResult, error) {
			return systemprompt.ToolProviderResult{Schemas: []llm.ToolSchema{{
				Name: "scoped", Description: "s", Parameters: json.RawMessage(`{}`),
			}}}, nil
		}); err != nil {
		t.Fatal(err)
	}
	if _, err := systemprompt.OnAssemble(childScope,
		func(requestContext context.Context, assembled *systemprompt.PromptAssembly, _ systemprompt.AssembleContext, downstream systemprompt.AssembleNext) (systemprompt.PromptAssembly, error) {
			assembled.Sections = append(assembled.Sections, systemprompt.AssembledSection{Name: "listener", Text: "Scoped listener."})
			return downstream(requestContext)
		}); err != nil {
		t.Fatal(err)
	}

	global := observePrompt(t, promptService, systemprompt.AssembleContext{})
	scoped := observePrompt(t, promptService, systemprompt.AssembleContext{Scope: childScope.Target()})
	if err := childRelease(requestContext); err != nil {
		t.Fatal(err)
	}
	afterDispose := observePrompt(t, promptService, systemprompt.AssembleContext{Scope: childScope.Target()})

	complete := contractCompletePrompt(t)
	suppressed, providerCalls := contractSuppressedPrompt(t)
	rendering := map[string]renderObservation{
		"singlePass": renderContractCase("Mode {{mode}}.", map[string]systemprompt.VariableValue{
			"mode": {Value: "{{other}}", Defined: true},
		}),
		"loneOpen":  renderContractCase("literal {{ prose", map[string]systemprompt.VariableValue{}),
		"unknown":   renderContractCase("{{other}}", map[string]systemprompt.VariableValue{}),
		"undefined": renderContractCase("{{unset}}", map[string]systemprompt.VariableValue{"unset": {}}),
		"malformed": renderContractCase("{{bad-name}}", map[string]systemprompt.VariableValue{}),
	}
	goOutput, err := json.Marshal(systemPromptContractObservation{
		Global: global, Scoped: scoped, AfterDispose: afterDispose,
		Complete: complete, Suppressed: suppressed, SuppressedProviderCalls: providerCalls,
		Rendering: rendering,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, goOutput, sourceOutput)
}

func observePrompt(t *testing.T, promptService systemprompt.SystemPrompt, assemblyContext systemprompt.AssembleContext) promptObservation {
	t.Helper()
	assembled, err := promptService.Assemble(context.Background(), assemblyContext)
	if err != nil {
		t.Fatal(err)
	}
	promptText, err := systemprompt.RenderPrompt(assembled)
	if err != nil {
		t.Fatal(err)
	}
	contextText, err := systemprompt.RenderContextSnapshot(assembled)
	if err != nil {
		t.Fatal(err)
	}
	variables := make(map[string]*string, len(assembled.Variables))
	for name, retained := range assembled.Variables {
		if retained.Defined {
			copied := retained.Value
			variables[name] = &copied
		} else {
			variables[name] = nil
		}
	}
	return promptObservation{
		Sections: assembled.Sections, Contexts: assembled.Contexts, Tools: assembled.Tools, Variables: variables,
		Prompt: promptText, ContextSnapshot: contextText,
	}
}

func contractCompletePrompt(t *testing.T) promptObservation {
	t.Helper()
	var promptService systemprompt.SystemPrompt
	var providerScope *plugin.Scope
	engine := plugin.NewRuntime()
	if _, err := engine.Load(context.Background(), promptContractPlugin{
		ready: func(available systemprompt.SystemPrompt, pluginScope *plugin.Scope) error {
			promptService = available
			providerScope = pluginScope
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := promptService.Section(context.Background(), providerScope, systemprompt.PromptSection{
		Name: "complete", Order: 50, Text: systemprompt.StaticText("Complete prompt."), Complete: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := systemprompt.OnAssemble(providerScope,
		func(requestContext context.Context, assembled *systemprompt.PromptAssembly, _ systemprompt.AssembleContext, downstream systemprompt.AssembleNext) (systemprompt.PromptAssembly, error) {
			assembled.Sections = append(assembled.Sections, systemprompt.AssembledSection{Name: "late", Text: "Late section."})
			return downstream(requestContext)
		}); err != nil {
		t.Fatal(err)
	}
	return observePrompt(t, promptService, systemprompt.AssembleContext{})
}

func contractSuppressedPrompt(t *testing.T) (promptObservation, int) {
	t.Helper()
	var promptService systemprompt.SystemPrompt
	var providerScope *plugin.Scope
	providerCalls := 0
	engine := plugin.NewRuntime()
	if _, err := engine.Load(context.Background(), promptContractPlugin{
		settings: systemprompt.Config{IncludeRuntimeContext: boolContractPointer(false)},
		ready: func(available systemprompt.SystemPrompt, pluginScope *plugin.Scope) error {
			promptService = available
			providerScope = pluginScope
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := promptService.Context(context.Background(), providerScope, systemprompt.PromptContext{
		Name: "policy", Order: 1,
		Text: systemprompt.TextFunc(func(context.Context, systemprompt.AssembleContext) (string, error) {
			providerCalls++
			return "policy", nil
		}),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := systemprompt.OnAssemble(providerScope,
		func(requestContext context.Context, assembled *systemprompt.PromptAssembly, _ systemprompt.AssembleContext, downstream systemprompt.AssembleNext) (systemprompt.PromptAssembly, error) {
			assembled.Contexts = append(assembled.Contexts, systemprompt.AssembledContext{Name: "late", Text: "Late context."})
			return downstream(requestContext)
		}); err != nil {
		t.Fatal(err)
	}
	return observePrompt(t, promptService, systemprompt.AssembleContext{}), providerCalls
}

func boolContractPointer(selected bool) *bool {
	return &selected
}

func renderContractCase(input string, variables map[string]systemprompt.VariableValue) renderObservation {
	resolved, err := systemprompt.RenderPrompt(systemprompt.PromptAssembly{
		Sections: []systemprompt.AssembledSection{{Name: "fixture", Text: input}}, Variables: variables,
	})
	if err != nil {
		return renderObservation{OK: false}
	}
	return renderObservation{OK: true, Value: resolved}
}
