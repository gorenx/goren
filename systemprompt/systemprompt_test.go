package systemprompt_test

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/systemprompt"
)

func mountPrompt(
	testingContext *testing.T,
	settings systemprompt.Config,
	options systemprompt.RegistryOptions,
) (*plugin.Runtime, *systemprompt.Registry, plugin.Handle) {
	testingContext.Helper()
	validated, err := systemprompt.ValidateConfig(settings)
	if err != nil {
		testingContext.Fatal(err)
	}
	prompts := systemprompt.New(validated, options)
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	handles, err := runtimeEngine.Start(context.Background(), prompts)
	if err != nil {
		testingContext.Fatal(err)
	}
	return runtimeEngine, prompts, handles[0]
}

func boolPointer(selected bool) *bool {
	return &selected
}

func TestSystemPromptAssemblesBuiltinsProvidersAndTools(t *testing.T) {
	t.Parallel()
	_, prompts, _ := mountPrompt(
		t,
		systemprompt.Config{
			Persona: "Mode: {{mode}}.",
			ToolOrder: []string{
				"todo",
				systemprompt.ToolOrderRest,
				"bash",
			},
		},
		systemprompt.RegistryOptions{},
	)
	requestContext := context.Background()
	if err := prompts.AddSection(requestContext, systemprompt.PromptSection{
		Name:  "rules",
		Order: 10,
		Text:  systemprompt.StaticText("Be precise."),
	}); err != nil {
		t.Fatal(err)
	}
	dynamicCalls := 0
	if err := prompts.AddSection(requestContext, systemprompt.PromptSection{
		Name:  "cwd",
		Order: 20,
		Text: systemprompt.TextFunc(func(
			context.Context,
			systemprompt.AssembleContext,
		) (string, error) {
			dynamicCalls++
			return "cwd: /tmp", nil
		}),
	}); err != nil {
		t.Fatal(err)
	}
	if err := prompts.AddContext(requestContext, systemprompt.PromptContext{
		Name:  "policy",
		Order: 1,
		Text:  systemprompt.StaticText("context {{mode}}"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := prompts.AddVariable(
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
	parameters := json.RawMessage(`{"type":"object"}`)
	if err := prompts.AddToolProvider(
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
						Parameters:  parameters,
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

	assembled, err := prompts.Assemble(
		requestContext,
		systemprompt.AssembleContext{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if dynamicCalls != 1 {
		t.Fatalf("dynamic provider calls = %d", dynamicCalls)
	}
	sectionNames := make([]string, len(assembled.Sections))
	for index, entry := range assembled.Sections {
		sectionNames[index] = entry.Name
	}
	if want := []string{
		"harness:identity",
		"deployment:persona",
		"rules",
		"cwd",
	}; !reflect.DeepEqual(sectionNames, want) {
		t.Fatalf("section names = %#v, want %#v", sectionNames, want)
	}
	promptText, err := systemprompt.RenderPrompt(assembled)
	if err != nil {
		t.Fatal(err)
	}
	wantPrompt := "You are an AI agent powered by DeepSeek Harness.\n\nMode: strict.\n\nBe precise.\n\ncwd: /tmp"
	if promptText != wantPrompt {
		t.Fatalf("rendered prompt = %q", promptText)
	}
	contextText, err := systemprompt.RenderContextSnapshot(assembled)
	if err != nil {
		t.Fatal(err)
	}
	wantContext := "Current runtime context. This snapshot supersedes earlier runtime-context snapshots.\n\ncontext strict"
	if contextText != wantContext {
		t.Fatalf("rendered context = %q", contextText)
	}
	schemaNames := make([]string, len(assembled.Tools))
	for index, schema := range assembled.Tools {
		schemaNames[index] = schema.Name
	}
	if want := []string{
		"todo",
		"alpha",
		"zeta",
		"bash",
	}; !reflect.DeepEqual(schemaNames, want) {
		t.Fatalf("tool names = %#v, want %#v", schemaNames, want)
	}
	parameters[1] = 'X'
	if string(assembled.Tools[3].Parameters) != `{"type":"object"}` {
		t.Fatalf(
			"tool parameters borrowed provider memory: %s",
			assembled.Tools[3].Parameters,
		)
	}
}

func TestSystemPromptOverlayShadowsAncestorLayer(t *testing.T) {
	t.Parallel()
	runtimeEngine, rootPrompts, rootHandle := mountPrompt(
		t,
		systemprompt.Config{
			Persona: "global persona",
		},
		systemprompt.RegistryOptions{},
	)
	requestContext := context.Background()
	globalTextCalls := 0
	if err := rootPrompts.AddSection(requestContext, systemprompt.PromptSection{
		Name:  "shared",
		Order: 5,
		Text: systemprompt.TextFunc(func(
			context.Context,
			systemprompt.AssembleContext,
		) (string, error) {
			globalTextCalls++
			return "global shared", nil
		}),
	}); err != nil {
		t.Fatal(err)
	}
	globalVariableCalls := 0
	if err := rootPrompts.AddVariable(
		requestContext,
		"mode",
		systemprompt.VariableProviderFunc(func(
			context.Context,
			systemprompt.AssembleContext,
		) (systemprompt.VariableValue, error) {
			globalVariableCalls++
			return systemprompt.VariableValue{
				Value:   "global",
				Defined: true,
			}, nil
		}),
	); err != nil {
		t.Fatal(err)
	}
	overlay := systemprompt.NewOverlay(systemprompt.RegistryOptions{})
	overlayHandle, err := runtimeEngine.MountChild(
		requestContext,
		rootHandle,
		overlay,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := overlay.AddSection(requestContext, systemprompt.PromptSection{
		Name:  systemprompt.PersonaSection,
		Order: systemprompt.PersonaOrder,
		Text:  systemprompt.StaticText("scoped persona {{mode}}"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := overlay.AddSection(requestContext, systemprompt.PromptSection{
		Name:  "shared",
		Order: 5,
		Text:  systemprompt.StaticText("scoped shared"),
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

	scoped, err := overlay.Assemble(
		requestContext,
		systemprompt.AssembleContext{},
	)
	if err != nil {
		t.Fatal(err)
	}
	scopedText, err := systemprompt.RenderPrompt(scoped)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(scopedText, "scoped persona strict") ||
		!strings.Contains(scopedText, "scoped shared") ||
		strings.Contains(scopedText, "global persona") {
		t.Fatalf("scoped prompt = %q", scopedText)
	}
	if globalTextCalls != 0 {
		t.Fatalf("shadowed global section provider calls = %d", globalTextCalls)
	}
	if globalVariableCalls != 1 {
		t.Fatalf("ancestor variable provider calls = %d", globalVariableCalls)
	}
	if err := runtimeEngine.Unload(requestContext, overlayHandle); err != nil {
		t.Fatal(err)
	}
	global, err := rootPrompts.Assemble(
		requestContext,
		systemprompt.AssembleContext{},
	)
	if err != nil {
		t.Fatal(err)
	}
	globalText, err := systemprompt.RenderPrompt(global)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(globalText, "global persona") ||
		!strings.Contains(globalText, "global shared") ||
		strings.Contains(globalText, "scoped") {
		t.Fatalf("global prompt after overlay unload = %q", globalText)
	}
}

type changedObserver struct {
	plugin.Base
	failNext bool
	error    error
	count    int
}

func (*changedObserver) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "system-prompt-change-observer",
		Events: []plugin.EventSubscription{
			plugin.EventOf[systemprompt.Changed](),
		},
	}
}

func (*changedObserver) Apply(context.Context) error {
	return nil
}

func (*changedObserver) Dispose(context.Context) error {
	return nil
}

func (observer *changedObserver) ObserveEvent(
	context.Context,
	plugin.Event,
) error {
	observer.count++
	if observer.failNext {
		observer.failNext = false
		return observer.error
	}
	return nil
}

func TestSystemPromptChangedFailureRollsBackAddition(t *testing.T) {
	t.Parallel()
	validated, err := systemprompt.ValidateConfig(systemprompt.Config{})
	if err != nil {
		t.Fatal(err)
	}
	prompts := systemprompt.New(validated, systemprompt.RegistryOptions{})
	sentinel := errors.New("change failed")
	observer := &changedObserver{
		failNext: true,
		error:    sentinel,
	}
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	if _, err := runtimeEngine.Start(
		context.Background(),
		prompts,
		observer,
	); err != nil {
		t.Fatal(err)
	}
	if err := prompts.AddSection(
		context.Background(),
		systemprompt.PromptSection{
			Name:  "leak",
			Order: 1,
			Text:  systemprompt.StaticText("leak"),
		},
	); !errors.Is(err, sentinel) {
		t.Fatalf("add error = %v", err)
	}
	assembled, err := prompts.Assemble(
		context.Background(),
		systemprompt.AssembleContext{},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range assembled.Sections {
		if entry.Name == "leak" {
			t.Fatal("failed addition leaked into assembly")
		}
	}
}

type appendSectionMiddleware struct {
	name string
	text string
}

func (middleware *appendSectionMiddleware) Intercept(
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
	assembled.Sections = append(assembled.Sections, systemprompt.AssembledSection{
		Name: middleware.name,
		Text: middleware.text,
	})
	return assembled, nil
}

type replaceAssemblyMiddleware struct{}

func (*replaceAssemblyMiddleware) Intercept(
	context.Context,
	systemprompt.AssembleRequest,
	plugin.WaterfallAction[
		systemprompt.AssembleRequest,
		systemprompt.PromptAssembly,
	],
) (systemprompt.PromptAssembly, error) {
	return systemprompt.PromptAssembly{
		Sections: []systemprompt.AssembledSection{
			{
				Name: "replacement",
				Text: "replacement",
			},
		},
		Contexts: []systemprompt.AssembledContext{
			{
				Name: "late",
				Text: "late",
			},
		},
		Variables: map[string]systemprompt.VariableValue{},
	}, nil
}

func TestSystemPromptCompleteAndSuppressionSurviveWaterfall(t *testing.T) {
	t.Parallel()
	_, completePrompts, _ := mountPrompt(
		t,
		systemprompt.Config{},
		systemprompt.RegistryOptions{
			Middleware: &appendSectionMiddleware{
				name: "late",
				text: "late",
			},
		},
	)
	if err := completePrompts.AddSection(
		context.Background(),
		systemprompt.PromptSection{
			Name:     "complete",
			Order:    50,
			Text:     systemprompt.StaticText("complete prompt"),
			Complete: true,
		},
	); err != nil {
		t.Fatal(err)
	}
	complete, err := completePrompts.Assemble(
		context.Background(),
		systemprompt.AssembleContext{},
	)
	if err != nil {
		t.Fatal(err)
	}
	wantComplete := []systemprompt.AssembledSection{
		{
			Name: "complete",
			Text: "complete prompt",
		},
	}
	if !reflect.DeepEqual(complete.Sections, wantComplete) {
		t.Fatalf("complete sections = %#v", complete.Sections)
	}

	_, suppressedPrompts, _ := mountPrompt(
		t,
		systemprompt.Config{
			IncludeRuntimeContext: boolPointer(false),
		},
		systemprompt.RegistryOptions{
			Middleware: &replaceAssemblyMiddleware{},
		},
	)
	providerCalls := 0
	if err := suppressedPrompts.AddContext(
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
		t.Fatal(err)
	}
	suppressed, err := suppressedPrompts.Assemble(
		context.Background(),
		systemprompt.AssembleContext{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if providerCalls != 0 || len(suppressed.Contexts) != 0 {
		t.Fatalf(
			"suppressed context = %#v, provider calls = %d",
			suppressed.Contexts,
			providerCalls,
		)
	}
}

func TestSystemPromptProviderMembershipIsSnapshotted(t *testing.T) {
	t.Parallel()
	_, prompts, _ := mountPrompt(
		t,
		systemprompt.Config{},
		systemprompt.RegistryOptions{},
	)
	requestContext := context.Background()
	added := false
	if err := prompts.AddToolProvider(
		requestContext,
		"first",
		systemprompt.ToolProviderFunc(func(
			context.Context,
			systemprompt.AssembleContext,
		) (systemprompt.ToolProviderResult, error) {
			if !added {
				added = true
				if err := prompts.AddToolProvider(
					requestContext,
					"late",
					systemprompt.ToolProviderFunc(func(
						context.Context,
						systemprompt.AssembleContext,
					) (systemprompt.ToolProviderResult, error) {
						return systemprompt.ToolProviderResult{
							Schemas: []llm.ToolSchema{
								{
									Name:       "late",
									Parameters: json.RawMessage(`{}`),
								},
							},
						}, nil
					}),
				); err != nil {
					return systemprompt.ToolProviderResult{}, err
				}
			}
			return systemprompt.ToolProviderResult{
				Schemas: []llm.ToolSchema{
					{
						Name:       "first",
						Parameters: json.RawMessage(`{}`),
					},
				},
			}, nil
		}),
	); err != nil {
		t.Fatal(err)
	}
	first, err := prompts.Assemble(
		requestContext,
		systemprompt.AssembleContext{},
	)
	if err != nil {
		t.Fatal(err)
	}
	second, err := prompts.Assemble(
		requestContext,
		systemprompt.AssembleContext{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Tools) != 1 || first.Tools[0].Name != "first" {
		t.Fatalf("first provider snapshot = %#v", first.Tools)
	}
	if len(second.Tools) != 2 || second.Tools[0].Name != "first" ||
		second.Tools[1].Name != "late" {
		t.Fatalf("second provider snapshot = %#v", second.Tools)
	}
}

func TestSystemPromptValidation(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		label    string
		settings systemprompt.Config
		want     string
	}{
		{
			label: "missing rest",
			settings: systemprompt.Config{
				ToolOrder: []string{},
			},
			want: "must contain",
		},
		{
			label: "duplicate",
			settings: systemprompt.Config{
				ToolOrder: []string{
					"bash",
					"bash",
					systemprompt.ToolOrderRest,
				},
			},
			want: "more than once",
		},
	} {
		testCase := testCase
		t.Run(testCase.label, func(t *testing.T) {
			t.Parallel()
			if _, err := systemprompt.ValidateConfig(testCase.settings); err == nil ||
				!strings.Contains(err.Error(), testCase.want) {
				t.Fatalf(
					"validation error = %v, want containing %q",
					err,
					testCase.want,
				)
			}
		})
	}
	_, prompts, _ := mountPrompt(
		t,
		systemprompt.Config{},
		systemprompt.RegistryOptions{},
	)
	if err := prompts.AddSection(
		context.Background(),
		systemprompt.PromptSection{
			Name:  "nan",
			Order: math.NaN(),
			Text:  systemprompt.StaticText("x"),
		},
	); err == nil || !strings.Contains(err.Error(), "finite") {
		t.Fatalf("non-finite section error = %v", err)
	}
	if err := prompts.AddVariable(
		context.Background(),
		"Bad-Name",
		systemprompt.VariableProviderFunc(func(
			context.Context,
			systemprompt.AssembleContext,
		) (systemprompt.VariableValue, error) {
			return systemprompt.VariableValue{}, nil
		}),
	); err == nil || !strings.Contains(err.Error(), "invalid prompt variable") {
		t.Fatalf("variable name error = %v", err)
	}
	if err := prompts.AddToolProvider(
		context.Background(),
		"reserved",
		systemprompt.ToolProviderFunc(func(
			context.Context,
			systemprompt.AssembleContext,
		) (systemprompt.ToolProviderResult, error) {
			return systemprompt.ToolProviderResult{
				Schemas: []llm.ToolSchema{
					{
						Name:       systemprompt.ToolOrderRest,
						Parameters: json.RawMessage(`{}`),
					},
				},
			}, nil
		}),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := prompts.Assemble(
		context.Background(),
		systemprompt.AssembleContext{},
	); err == nil || !strings.Contains(err.Error(), "reserved tool name") {
		t.Fatalf("reserved tool error = %v", err)
	}
}

func TestRenderPromptUsesStrictSinglePassInterpolation(t *testing.T) {
	t.Parallel()
	base := systemprompt.PromptAssembly{
		Variables: map[string]systemprompt.VariableValue{
			"mode": {
				Value:   "{{other}}",
				Defined: true,
			},
			"empty": {
				Value:   "",
				Defined: true,
			},
			"unset": {},
		},
	}
	for _, testCase := range []struct {
		label       string
		input       string
		want        string
		wantMessage string
	}{
		{
			label: "single pass",
			input: "Mode {{mode}}.",
			want:  "Mode {{other}}.",
		},
		{
			label: "empty",
			input: "A{{empty}}B",
			want:  "AB",
		},
		{
			label: "literal lone open",
			input: "literal {{ prose",
			want:  "literal {{ prose",
		},
		{
			label:       "unknown",
			input:       "{{other}}",
			wantMessage: "unknown prompt variable",
		},
		{
			label:       "undefined",
			input:       "{{unset}}",
			wantMessage: "has no value",
		},
		{
			label:       "malformed",
			input:       "{{bad-name}}",
			wantMessage: "malformed prompt variable",
		},
		{
			label:       "incomplete group",
			input:       "{{bad} tail }}",
			wantMessage: "complete simple",
		},
	} {
		testCase := testCase
		t.Run(testCase.label, func(t *testing.T) {
			t.Parallel()
			assembled := base
			assembled.Sections = []systemprompt.AssembledSection{
				{
					Name: "fixture",
					Text: testCase.input,
				},
			}
			actual, err := systemprompt.RenderPrompt(assembled)
			if testCase.wantMessage != "" {
				if err == nil || !strings.Contains(err.Error(), testCase.wantMessage) {
					t.Fatalf(
						"RenderPrompt error = %v, want containing %q",
						err,
						testCase.wantMessage,
					)
				}
				return
			}
			if err != nil || actual != testCase.want {
				t.Fatalf(
					"RenderPrompt = (%q, %v), want %q",
					actual,
					err,
					testCase.want,
				)
			}
		})
	}
}

func TestSystemPromptConfigCanDisableBuiltins(t *testing.T) {
	t.Parallel()
	_, prompts, _ := mountPrompt(
		t,
		systemprompt.Config{
			IncludeHarnessIdentity: boolPointer(false),
			IncludeRuntimeContext:  boolPointer(false),
			Persona:                "deployment",
		},
		systemprompt.RegistryOptions{},
	)
	assembled, err := prompts.Assemble(
		context.Background(),
		systemprompt.AssembleContext{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(assembled.Sections) != 1 ||
		assembled.Sections[0].Name != systemprompt.PersonaSection ||
		len(assembled.Contexts) != 0 {
		t.Fatalf("disabled builtins assembly = %#v", assembled)
	}
}

func TestToolProviderInChildLayerShadowsSameNamedParentProvider(t *testing.T) {
	t.Parallel()
	runtimeEngine, prompts, promptHandle := mountPrompt(
		t,
		systemprompt.Config{},
		systemprompt.RegistryOptions{},
	)
	t.Cleanup(func() {
		if err := runtimeEngine.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	requestContext := context.Background()
	rootCalls := 0
	if err := prompts.AddToolProvider(
		requestContext,
		"shared",
		systemprompt.ToolProviderFunc(func(
			context.Context,
			systemprompt.AssembleContext,
		) (systemprompt.ToolProviderResult, error) {
			rootCalls++
			return systemprompt.ToolProviderResult{
				Schemas: []llm.ToolSchema{
					{
						Name:       "root-tool",
						Parameters: json.RawMessage(`{}`),
					},
				},
			}, nil
		}),
	); err != nil {
		t.Fatal(err)
	}
	overlay := systemprompt.NewOverlay(systemprompt.RegistryOptions{})
	if _, err := runtimeEngine.MountChild(
		requestContext,
		promptHandle,
		overlay,
	); err != nil {
		t.Fatal(err)
	}
	overlayCalls := 0
	if err := overlay.AddToolProvider(
		requestContext,
		"shared",
		systemprompt.ToolProviderFunc(func(
			context.Context,
			systemprompt.AssembleContext,
		) (systemprompt.ToolProviderResult, error) {
			overlayCalls++
			return systemprompt.ToolProviderResult{
				Schemas: []llm.ToolSchema{
					{
						Name:       "child-tool",
						Parameters: json.RawMessage(`{}`),
					},
				},
			}, nil
		}),
	); err != nil {
		t.Fatal(err)
	}
	assembled, err := overlay.Assemble(
		requestContext,
		systemprompt.AssembleContext{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if rootCalls != 0 || overlayCalls != 1 || len(assembled.Tools) != 1 ||
		assembled.Tools[0].Name != "child-tool" {
		t.Fatalf(
			"calls = (%d, %d), tools = %#v",
			rootCalls,
			overlayCalls,
			assembled.Tools,
		)
	}
}
