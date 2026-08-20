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

type contractAssemblyMiddleware struct {
	section *systemprompt.AssembledSection
	context *systemprompt.AssembledContext
}

func (middleware *contractAssemblyMiddleware) Intercept(
	requestContext context.Context,
	request systemprompt.AssembleRequest,
	downstream plugin.WaterfallAction[
		systemprompt.AssembleRequest,
		systemprompt.PromptAssembly,
	],
) (systemprompt.PromptAssembly, error) {
	assembled, err := downstream.Execute(requestContext, request)
	if err != nil {
		return systemprompt.PromptAssembly{}, err
	}
	if middleware.section != nil {
		assembled.Sections = append(
			assembled.Sections,
			*middleware.section,
		)
	}
	if middleware.context != nil {
		assembled.Contexts = append(
			assembled.Contexts,
			*middleware.context,
		)
	}
	return assembled, nil
}

func TestPinnedSourceSystemPromptMatchesGo(t *testing.T) {
	repositoryRoot, sourceRoot := contractPaths(t)
	commandContext, cancel := context.WithTimeout(
		context.Background(),
		30*time.Second,
	)
	defer cancel()
	sourceOutput, err := runTypeScript(
		commandContext,
		sourceRoot,
		filepath.Join(
			repositoryRoot,
			"tests",
			"contract",
			"typescript",
			"system-prompt.ts",
		),
		sourceRoot,
		filepath.Join(
			repositoryRoot,
			"contracts",
			"deepseek-harness",
			"manifest.json",
		),
	)
	if err != nil {
		t.Fatal(err)
	}

	requestContext := context.Background()
	validated, err := systemprompt.ValidateConfig(systemprompt.Config{
		Persona: "Mode: {{mode}}.",
		ToolOrder: []string{
			"todo",
			systemprompt.ToolOrderRest,
			"bash",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	rootPrompts := systemprompt.New(
		validated,
		systemprompt.RegistryOptions{},
	)
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	handles, err := runtimeEngine.Start(requestContext, rootPrompts)
	if err != nil {
		t.Fatal(err)
	}
	if err := rootPrompts.AddSection(requestContext, systemprompt.PromptSection{
		Name:  "rules",
		Order: 10,
		Text:  systemprompt.StaticText("Be precise."),
	}); err != nil {
		t.Fatal(err)
	}
	if err := rootPrompts.AddSection(requestContext, systemprompt.PromptSection{
		Name:  "cwd",
		Order: 20,
		Text:  systemprompt.StaticText("cwd: /tmp"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := rootPrompts.AddContext(requestContext, systemprompt.PromptContext{
		Name:  "later",
		Order: 20,
		Text:  systemprompt.StaticText("context 2"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := rootPrompts.AddContext(requestContext, systemprompt.PromptContext{
		Name:  "earlier",
		Order: 10,
		Text:  systemprompt.StaticText("context {{mode}}"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := rootPrompts.AddVariable(
		requestContext,
		"mode",
		systemprompt.VariableProviderFunc(func(
			context.Context,
			systemprompt.AssembleContext,
		) (systemprompt.VariableValue, error) {
			return systemprompt.VariableValue{
				Value:   "normal",
				Defined: true,
			}, nil
		}),
	); err != nil {
		t.Fatal(err)
	}
	if err := rootPrompts.AddToolProvider(
		requestContext,
		"tools",
		systemprompt.ToolProviderFunc(func(
			context.Context,
			systemprompt.AssembleContext,
		) (systemprompt.ToolProviderResult, error) {
			return systemprompt.ToolProviderResult{
				Schemas: []llm.ToolSchema{
					{
						Name:        "bash",
						Description: "shell",
						Parameters:  json.RawMessage(`{"type":"object"}`),
					},
					{
						Name:        "zeta",
						Description: "z",
						Parameters:  json.RawMessage(`{}`),
					},
					{
						Name:        "todo",
						Description: "tasks",
						Parameters:  json.RawMessage(`{}`),
					},
					{
						Name:        "alpha",
						Description: "a",
						Parameters:  json.RawMessage(`{}`),
					},
				},
			}, nil
		}),
	); err != nil {
		t.Fatal(err)
	}

	overlay := systemprompt.NewOverlay(systemprompt.RegistryOptions{
		Middleware: &contractAssemblyMiddleware{
			section: &systemprompt.AssembledSection{
				Name: "listener",
				Text: "Scoped listener.",
			},
		},
	})
	overlayHandle, err := runtimeEngine.MountScopedChild(
		requestContext,
		handles[0],
		overlay,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := overlay.AddSection(requestContext, systemprompt.PromptSection{
		Name:  systemprompt.PersonaSection,
		Order: systemprompt.PersonaOrder,
		Text:  systemprompt.StaticText("Scoped {{mode}}."),
	}); err != nil {
		t.Fatal(err)
	}
	if err := overlay.AddSection(requestContext, systemprompt.PromptSection{
		Name:  "child",
		Order: 15,
		Text:  systemprompt.StaticText("Child section."),
	}); err != nil {
		t.Fatal(err)
	}
	if err := overlay.AddVariable(
		requestContext,
		"mode",
		systemprompt.VariableProviderFunc(func(
			context.Context,
			systemprompt.AssembleContext,
		) (systemprompt.VariableValue, error) {
			return systemprompt.VariableValue{
				Value:   "strict",
				Defined: true,
			}, nil
		}),
	); err != nil {
		t.Fatal(err)
	}
	if err := overlay.AddToolProvider(
		requestContext,
		"scoped-tools",
		systemprompt.ToolProviderFunc(func(
			context.Context,
			systemprompt.AssembleContext,
		) (systemprompt.ToolProviderResult, error) {
			return systemprompt.ToolProviderResult{
				Schemas: []llm.ToolSchema{
					{
						Name:        "scoped",
						Description: "s",
						Parameters:  json.RawMessage(`{}`),
					},
				},
			}, nil
		}),
	); err != nil {
		t.Fatal(err)
	}

	global := observePrompt(t, rootPrompts)
	scoped := observePrompt(t, overlay)
	if err := runtimeEngine.Unload(requestContext, overlayHandle); err != nil {
		t.Fatal(err)
	}
	afterDispose := observePrompt(t, rootPrompts)

	complete := contractCompletePrompt(t)
	suppressed, providerCalls := contractSuppressedPrompt(t)
	rendering := map[string]renderObservation{
		"singlePass": renderContractCase(
			"Mode {{mode}}.",
			map[string]systemprompt.VariableValue{
				"mode": {
					Value:   "{{other}}",
					Defined: true,
				},
			},
		),
		"loneOpen": renderContractCase(
			"literal {{ prose",
			map[string]systemprompt.VariableValue{},
		),
		"unknown": renderContractCase(
			"{{other}}",
			map[string]systemprompt.VariableValue{},
		),
		"undefined": renderContractCase(
			"{{unset}}",
			map[string]systemprompt.VariableValue{
				"unset": {},
			},
		),
		"malformed": renderContractCase(
			"{{bad-name}}",
			map[string]systemprompt.VariableValue{},
		),
	}
	goOutput, err := json.Marshal(systemPromptContractObservation{
		Global:                  global,
		Scoped:                  scoped,
		AfterDispose:            afterDispose,
		Complete:                complete,
		Suppressed:              suppressed,
		SuppressedProviderCalls: providerCalls,
		Rendering:               rendering,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, goOutput, sourceOutput)
}

func observePrompt(
	testingContext *testing.T,
	prompts systemprompt.Assembler,
) promptObservation {
	testingContext.Helper()
	assembled, err := prompts.Assemble(
		context.Background(),
		systemprompt.AssembleContext{},
	)
	if err != nil {
		testingContext.Fatal(err)
	}
	promptText, err := systemprompt.RenderPrompt(assembled)
	if err != nil {
		testingContext.Fatal(err)
	}
	contextText, err := systemprompt.RenderContextSnapshot(assembled)
	if err != nil {
		testingContext.Fatal(err)
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
		Sections:        assembled.Sections,
		Contexts:        assembled.Contexts,
		Tools:           assembled.Tools,
		Variables:       variables,
		Prompt:          promptText,
		ContextSnapshot: contextText,
	}
}

func contractCompletePrompt(testingContext *testing.T) promptObservation {
	testingContext.Helper()
	validated, err := systemprompt.ValidateConfig(systemprompt.Config{})
	if err != nil {
		testingContext.Fatal(err)
	}
	prompts := systemprompt.New(
		validated,
		systemprompt.RegistryOptions{
			Middleware: &contractAssemblyMiddleware{
				section: &systemprompt.AssembledSection{
					Name: "late",
					Text: "Late section.",
				},
			},
		},
	)
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	if _, err := runtimeEngine.Start(context.Background(), prompts); err != nil {
		testingContext.Fatal(err)
	}
	if err := prompts.AddSection(
		context.Background(),
		systemprompt.PromptSection{
			Name:     "complete",
			Order:    50,
			Text:     systemprompt.StaticText("Complete prompt."),
			Complete: true,
		},
	); err != nil {
		testingContext.Fatal(err)
	}
	return observePrompt(testingContext, prompts)
}

func contractSuppressedPrompt(
	testingContext *testing.T,
) (promptObservation, int) {
	testingContext.Helper()
	providerCalls := 0
	validated, err := systemprompt.ValidateConfig(systemprompt.Config{
		IncludeRuntimeContext: boolContractPointer(false),
	})
	if err != nil {
		testingContext.Fatal(err)
	}
	prompts := systemprompt.New(
		validated,
		systemprompt.RegistryOptions{
			Middleware: &contractAssemblyMiddleware{
				context: &systemprompt.AssembledContext{
					Name: "late",
					Text: "Late context.",
				},
			},
		},
	)
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	if _, err := runtimeEngine.Start(context.Background(), prompts); err != nil {
		testingContext.Fatal(err)
	}
	if err := prompts.AddContext(
		context.Background(),
		systemprompt.PromptContext{
			Name:  "policy",
			Order: 1,
			Text: systemprompt.TextFunc(func(
				context.Context,
				systemprompt.AssembleContext,
			) (string, error) {
				providerCalls++
				return "policy", nil
			}),
		},
	); err != nil {
		testingContext.Fatal(err)
	}
	return observePrompt(testingContext, prompts), providerCalls
}

func boolContractPointer(selected bool) *bool {
	return &selected
}

func renderContractCase(
	input string,
	variables map[string]systemprompt.VariableValue,
) renderObservation {
	resolved, err := systemprompt.RenderPrompt(systemprompt.PromptAssembly{
		Sections: []systemprompt.AssembledSection{
			{
				Name: "fixture",
				Text: input,
			},
		},
		Variables: variables,
	})
	if err != nil {
		return renderObservation{
			OK: false,
		}
	}
	return renderObservation{
		OK:    true,
		Value: resolved,
	}
}
