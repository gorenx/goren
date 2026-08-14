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

type fixturePlugin struct {
	settings systemprompt.Config
	ready    func(systemprompt.SystemPrompt, *plugin.Scope) error
}

func (instance fixturePlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{Name: "system-prompt-fixture", Provides: []plugin.ServiceRef{systemprompt.Service.Ref()}}
}

func (instance fixturePlugin) Apply(requestContext context.Context, pluginScope *plugin.Scope) error {
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
	if instance.ready != nil {
		return instance.ready(promptService, pluginScope)
	}
	return nil
}

func mountPrompt(t *testing.T, settings systemprompt.Config) (*plugin.Runtime, systemprompt.SystemPrompt, *plugin.Scope) {
	t.Helper()
	engine := plugin.NewRuntime()
	var promptService systemprompt.SystemPrompt
	var providerScope *plugin.Scope
	_, err := engine.Load(context.Background(), fixturePlugin{
		settings: settings,
		ready: func(available systemprompt.SystemPrompt, pluginScope *plugin.Scope) error {
			promptService = available
			providerScope = pluginScope
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return engine, promptService, providerScope
}

func boolPointer(selected bool) *bool {
	return &selected
}

func TestSystemPromptAssemblesBuiltinsProvidersAndTools(t *testing.T) {
	t.Parallel()
	_, promptService, providerScope := mountPrompt(t, systemprompt.Config{
		Persona:   "Mode: {{mode}}.",
		ToolOrder: []string{"todo", systemprompt.ToolOrderRest, "bash"},
	})
	requestContext := context.Background()
	if _, err := promptService.Section(requestContext, providerScope, systemprompt.PromptSection{
		Name: "rules", Order: 10, Text: systemprompt.StaticText("Be precise."),
	}); err != nil {
		t.Fatal(err)
	}
	dynamicCalls := 0
	if _, err := promptService.Section(requestContext, providerScope, systemprompt.PromptSection{
		Name: "cwd", Order: 20,
		Text: systemprompt.TextFunc(func(context.Context, systemprompt.AssembleContext) (string, error) {
			dynamicCalls++
			return "cwd: /tmp", nil
		}),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := promptService.Context(requestContext, providerScope, systemprompt.PromptContext{
		Name: "policy", Order: 1, Text: systemprompt.StaticText("context {{mode}}"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := promptService.Variable(requestContext, providerScope, "mode",
		func(context.Context, systemprompt.AssembleContext) (systemprompt.VariableValue, error) {
			return systemprompt.VariableValue{Value: "strict", Defined: true}, nil
		}); err != nil {
		t.Fatal(err)
	}
	parameters := json.RawMessage(`{"type":"object"}`)
	if _, err := promptService.Tools(requestContext, providerScope,
		func(context.Context, systemprompt.AssembleContext) (systemprompt.ToolProviderResult, error) {
			return systemprompt.ToolProviderResult{Schemas: []llm.ToolSchema{
				{Name: "bash", Description: "shell", Parameters: parameters},
				{Name: "zeta", Description: "z", Parameters: json.RawMessage(`{}`)},
				{Name: "todo", Description: "tasks", Parameters: json.RawMessage(`{}`)},
				{Name: "alpha", Description: "a", Parameters: json.RawMessage(`{}`)},
			}}, nil
		}); err != nil {
		t.Fatal(err)
	}

	assembled, err := promptService.Assemble(requestContext, systemprompt.AssembleContext{})
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
	if want := []string{"harness:identity", "deployment:persona", "rules", "cwd"}; !reflect.DeepEqual(sectionNames, want) {
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
	if want := "Current runtime context. This snapshot supersedes earlier runtime-context snapshots.\n\ncontext strict"; contextText != want {
		t.Fatalf("rendered context = %q", contextText)
	}
	schemaNames := make([]string, len(assembled.Tools))
	for index, schema := range assembled.Tools {
		schemaNames[index] = schema.Name
	}
	if want := []string{"todo", "alpha", "zeta", "bash"}; !reflect.DeepEqual(schemaNames, want) {
		t.Fatalf("tool names = %#v, want %#v", schemaNames, want)
	}
	parameters[1] = 'X'
	if string(assembled.Tools[3].Parameters) != `{"type":"object"}` {
		t.Fatalf("tool parameters borrowed provider memory: %s", assembled.Tools[3].Parameters)
	}
}

func TestSystemPromptScopedLayersShadowAndDispose(t *testing.T) {
	t.Parallel()
	_, promptService, providerScope := mountPrompt(t, systemprompt.Config{Persona: "global persona"})
	requestContext := context.Background()
	globalTextCalls := 0
	if _, err := promptService.Section(requestContext, providerScope, systemprompt.PromptSection{
		Name: "shared", Order: 5,
		Text: systemprompt.TextFunc(func(context.Context, systemprompt.AssembleContext) (string, error) {
			globalTextCalls++
			return "global shared", nil
		}),
	}); err != nil {
		t.Fatal(err)
	}
	globalVariableCalls := 0
	if _, err := promptService.Variable(requestContext, providerScope, "mode",
		func(context.Context, systemprompt.AssembleContext) (systemprompt.VariableValue, error) {
			globalVariableCalls++
			return systemprompt.VariableValue{Value: "global", Defined: true}, nil
		}); err != nil {
		t.Fatal(err)
	}
	childScope, childRelease, err := providerScope.Child("agent")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := promptService.Section(requestContext, childScope, systemprompt.PromptSection{
		Name: systemprompt.PersonaSection, Order: 0, Text: systemprompt.StaticText("scoped persona {{mode}}"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := promptService.Section(requestContext, childScope, systemprompt.PromptSection{
		Name: "shared", Order: 5, Text: systemprompt.StaticText("scoped shared"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := promptService.Variable(requestContext, childScope, "mode",
		func(context.Context, systemprompt.AssembleContext) (systemprompt.VariableValue, error) {
			return systemprompt.VariableValue{Value: "strict", Defined: true}, nil
		}); err != nil {
		t.Fatal(err)
	}
	if _, err := systemprompt.OnAssemble(childScope,
		func(requestContext context.Context, assembled *systemprompt.PromptAssembly, _ systemprompt.AssembleContext, downstream systemprompt.AssembleNext) (systemprompt.PromptAssembly, error) {
			assembled.Sections = append(assembled.Sections, systemprompt.AssembledSection{Name: "listener", Text: "child listener"})
			return downstream(requestContext)
		}); err != nil {
		t.Fatal(err)
	}

	scoped, err := promptService.Assemble(requestContext, systemprompt.AssembleContext{Scope: childScope.Target()})
	if err != nil {
		t.Fatal(err)
	}
	scopedText, err := systemprompt.RenderPrompt(scoped)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(scopedText, "scoped persona strict") || !strings.Contains(scopedText, "scoped shared") ||
		!strings.Contains(scopedText, "child listener") || strings.Contains(scopedText, "global persona") {
		t.Fatalf("scoped prompt = %q", scopedText)
	}
	if globalTextCalls != 0 {
		t.Fatalf("shadowed global section provider calls = %d", globalTextCalls)
	}
	if globalVariableCalls != 1 {
		t.Fatalf("shadowed global variable provider calls = %d", globalVariableCalls)
	}
	global, err := promptService.Assemble(requestContext, systemprompt.AssembleContext{})
	if err != nil {
		t.Fatal(err)
	}
	globalText, err := systemprompt.RenderPrompt(global)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(globalText, "global persona") || !strings.Contains(globalText, "global shared") || strings.Contains(globalText, "child listener") {
		t.Fatalf("global prompt = %q", globalText)
	}
	if err := childRelease(requestContext); err != nil {
		t.Fatal(err)
	}
	after, err := promptService.Assemble(requestContext, systemprompt.AssembleContext{Scope: childScope.Target()})
	if err != nil {
		t.Fatal(err)
	}
	afterText, err := systemprompt.RenderPrompt(after)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(afterText, "scoped") || strings.Contains(afterText, "child listener") {
		t.Fatalf("disposed scoped contribution remains: %q", afterText)
	}
}

func TestSystemPromptChangeFailureRollsBackAndAssemblySnapshotsProviders(t *testing.T) {
	t.Parallel()
	_, promptService, providerScope := mountPrompt(t, systemprompt.Config{})
	requestContext := context.Background()
	sentinel := errors.New("change failed")
	failNext := true
	if _, err := systemprompt.OnChange(providerScope, func(context.Context) error {
		if failNext {
			failNext = false
			return sentinel
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := promptService.Section(requestContext, providerScope, systemprompt.PromptSection{
		Name: "leak", Order: 1, Text: systemprompt.StaticText("leak"),
	}); !errors.Is(err, sentinel) {
		t.Fatalf("registration error = %v", err)
	}
	assembled, err := promptService.Assemble(requestContext, systemprompt.AssembleContext{})
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range assembled.Sections {
		if entry.Name == "leak" {
			t.Fatal("failed registration leaked into assembly")
		}
	}

	added := false
	if _, err := promptService.Tools(requestContext, providerScope,
		func(context.Context, systemprompt.AssembleContext) (systemprompt.ToolProviderResult, error) {
			if !added {
				added = true
				if _, registrationErr := promptService.Tools(requestContext, providerScope,
					func(context.Context, systemprompt.AssembleContext) (systemprompt.ToolProviderResult, error) {
						return systemprompt.ToolProviderResult{Schemas: []llm.ToolSchema{{Name: "late", Parameters: json.RawMessage(`{}`)}}}, nil
					}); registrationErr != nil {
					return systemprompt.ToolProviderResult{}, registrationErr
				}
			}
			return systemprompt.ToolProviderResult{Schemas: []llm.ToolSchema{{Name: "first", Parameters: json.RawMessage(`{}`)}}}, nil
		}); err != nil {
		t.Fatal(err)
	}
	first, err := promptService.Assemble(requestContext, systemprompt.AssembleContext{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := promptService.Assemble(requestContext, systemprompt.AssembleContext{})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Tools) != 1 || first.Tools[0].Name != "first" {
		t.Fatalf("first provider snapshot = %#v", first.Tools)
	}
	if len(second.Tools) != 2 || second.Tools[0].Name != "first" || second.Tools[1].Name != "late" {
		t.Fatalf("second provider snapshot = %#v", second.Tools)
	}
}

func TestSystemPromptCompleteSuppressionAndPostWaterfallInvariant(t *testing.T) {
	t.Parallel()
	_, promptService, providerScope := mountPrompt(t, systemprompt.Config{})
	requestContext := context.Background()
	if _, err := promptService.Section(requestContext, providerScope, systemprompt.PromptSection{
		Name: "complete", Order: 50, Text: systemprompt.StaticText("complete prompt"), Complete: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := systemprompt.OnAssemble(providerScope,
		func(requestContext context.Context, assembled *systemprompt.PromptAssembly, _ systemprompt.AssembleContext, downstream systemprompt.AssembleNext) (systemprompt.PromptAssembly, error) {
			assembled.Sections = append(assembled.Sections, systemprompt.AssembledSection{Name: "late", Text: "late"})
			return downstream(requestContext)
		}); err != nil {
		t.Fatal(err)
	}
	assembled, err := promptService.Assemble(requestContext, systemprompt.AssembleContext{})
	if err != nil {
		t.Fatal(err)
	}
	if want := []systemprompt.AssembledSection{{Name: "complete", Text: "complete prompt"}}; !reflect.DeepEqual(assembled.Sections, want) {
		t.Fatalf("complete sections = %#v", assembled.Sections)
	}

	childScope, _, err := providerScope.Child("suppressed")
	if err != nil {
		t.Fatal(err)
	}
	providerCalls := 0
	if _, err := promptService.Context(requestContext, providerScope, systemprompt.PromptContext{
		Name: "policy", Order: 1,
		Text: systemprompt.TextFunc(func(context.Context, systemprompt.AssembleContext) (string, error) {
			providerCalls++
			return "policy", nil
		}),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := promptService.SuppressRuntimeContext(requestContext, childScope); err != nil {
		t.Fatal(err)
	}
	if _, err := systemprompt.OnAssemble(childScope,
		func(_ context.Context, _ *systemprompt.PromptAssembly, _ systemprompt.AssembleContext, _ systemprompt.AssembleNext) (systemprompt.PromptAssembly, error) {
			return systemprompt.PromptAssembly{
				Sections:  []systemprompt.AssembledSection{{Name: "replacement", Text: "replacement"}},
				Contexts:  []systemprompt.AssembledContext{{Name: "late", Text: "late"}},
				Variables: map[string]systemprompt.VariableValue{},
			}, nil
		}); err != nil {
		t.Fatal(err)
	}
	suppressed, err := promptService.Assemble(requestContext, systemprompt.AssembleContext{Scope: childScope.Target()})
	if err != nil {
		t.Fatal(err)
	}
	if providerCalls != 0 || len(suppressed.Contexts) != 0 {
		t.Fatalf("suppressed context = %#v, provider calls = %d", suppressed.Contexts, providerCalls)
	}
	if want := []systemprompt.AssembledSection{{Name: "complete", Text: "complete prompt"}}; !reflect.DeepEqual(suppressed.Sections, want) {
		t.Fatalf("complete section after short circuit = %#v", suppressed.Sections)
	}

	invalidScope, _, err := providerScope.Child("invalid")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := systemprompt.OnAssemble(invalidScope,
		func(requestContext context.Context, assembled *systemprompt.PromptAssembly, _ systemprompt.AssembleContext, downstream systemprompt.AssembleNext) (systemprompt.PromptAssembly, error) {
			assembled.Sections = append(assembled.Sections, systemprompt.AssembledSection{Name: assembled.Sections[0].Name, Text: "duplicate"})
			return downstream(requestContext)
		}); err != nil {
		t.Fatal(err)
	}
	if _, err := promptService.Assemble(requestContext, systemprompt.AssembleContext{Scope: invalidScope.Target()}); err == nil || !strings.Contains(err.Error(), "duplicated") {
		t.Fatalf("post-waterfall invariant error = %v", err)
	}
}

func TestSystemPromptRegistrationAndToolOrderValidation(t *testing.T) {
	t.Parallel()
	requestContext := context.Background()
	for _, testCase := range []struct {
		label    string
		settings systemprompt.Config
		want     string
	}{
		{label: "missing rest", settings: systemprompt.Config{ToolOrder: []string{}}, want: "must contain"},
		{label: "duplicate", settings: systemprompt.Config{ToolOrder: []string{"bash", "bash", systemprompt.ToolOrderRest}}, want: "more than once"},
	} {
		testCase := testCase
		t.Run(testCase.label, func(t *testing.T) {
			t.Parallel()
			engine := plugin.NewRuntime()
			if _, err := engine.Load(requestContext, fixturePlugin{settings: testCase.settings}); err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("load error = %v, want containing %q", err, testCase.want)
			}
		})
	}
	_, promptService, providerScope := mountPrompt(t, systemprompt.Config{})
	if _, err := promptService.Section(requestContext, providerScope, systemprompt.PromptSection{
		Name: "nan", Order: math.NaN(), Text: systemprompt.StaticText("x"),
	}); err == nil || !strings.Contains(err.Error(), "finite") {
		t.Fatalf("non-finite section error = %v", err)
	}
	if _, err := promptService.Variable(requestContext, providerScope, "Bad-Name",
		func(context.Context, systemprompt.AssembleContext) (systemprompt.VariableValue, error) {
			return systemprompt.VariableValue{}, nil
		}); err == nil || !strings.Contains(err.Error(), "invalid prompt variable") {
		t.Fatalf("variable name error = %v", err)
	}
	if _, err := promptService.Tools(requestContext, providerScope,
		func(context.Context, systemprompt.AssembleContext) (systemprompt.ToolProviderResult, error) {
			return systemprompt.ToolProviderResult{Schemas: []llm.ToolSchema{{Name: systemprompt.ToolOrderRest, Parameters: json.RawMessage(`{}`)}}}, nil
		}); err != nil {
		t.Fatal(err)
	}
	if _, err := promptService.Assemble(requestContext, systemprompt.AssembleContext{}); err == nil || !strings.Contains(err.Error(), "reserved tool name") {
		t.Fatalf("reserved tool error = %v", err)
	}
}

func TestRenderPromptUsesStrictSinglePassInterpolation(t *testing.T) {
	t.Parallel()
	base := systemprompt.PromptAssembly{Variables: map[string]systemprompt.VariableValue{
		"mode":  {Value: "{{other}}", Defined: true},
		"empty": {Value: "", Defined: true},
		"unset": {},
	}}
	for _, testCase := range []struct {
		label       string
		input       string
		want        string
		wantMessage string
	}{
		{label: "single pass", input: "Mode {{mode}}.", want: "Mode {{other}}."},
		{label: "empty", input: "A{{empty}}B", want: "AB"},
		{label: "literal lone open", input: "literal {{ prose", want: "literal {{ prose"},
		{label: "unknown", input: "{{other}}", wantMessage: "unknown prompt variable"},
		{label: "undefined", input: "{{unset}}", wantMessage: "has no value"},
		{label: "malformed", input: "{{bad-name}}", wantMessage: "malformed prompt variable"},
		{label: "incomplete group", input: "{{bad} tail }}", wantMessage: "complete simple"},
	} {
		testCase := testCase
		t.Run(testCase.label, func(t *testing.T) {
			t.Parallel()
			assembled := base
			assembled.Sections = []systemprompt.AssembledSection{{Name: "fixture", Text: testCase.input}}
			actual, err := systemprompt.RenderPrompt(assembled)
			if testCase.wantMessage != "" {
				if err == nil || !strings.Contains(err.Error(), testCase.wantMessage) {
					t.Fatalf("RenderPrompt error = %v, want containing %q", err, testCase.wantMessage)
				}
				return
			}
			if err != nil || actual != testCase.want {
				t.Fatalf("RenderPrompt = (%q, %v), want %q", actual, err, testCase.want)
			}
		})
	}
}

func TestSystemPromptConfigCanDisableBuiltins(t *testing.T) {
	t.Parallel()
	_, promptService, _ := mountPrompt(t, systemprompt.Config{
		IncludeHarnessIdentity: boolPointer(false),
		IncludeRuntimeContext:  boolPointer(false),
		Persona:                "deployment",
	})
	assembled, err := promptService.Assemble(context.Background(), systemprompt.AssembleContext{})
	if err != nil {
		t.Fatal(err)
	}
	if len(assembled.Sections) != 1 || assembled.Sections[0].Name != systemprompt.PersonaSection || len(assembled.Contexts) != 0 {
		t.Fatalf("disabled builtins assembly = %#v", assembled)
	}
}
