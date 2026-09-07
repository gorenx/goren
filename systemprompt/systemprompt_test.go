package systemprompt_test

import (
	"context"
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/systemprompt"
)

func newPrompts(t *testing.T, settings systemprompt.Config) *systemprompt.PromptLayer {
	t.Helper()
	validated, err := systemprompt.ValidateConfig(settings)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := systemprompt.NewLayer(validated)
	if err != nil {
		t.Fatal(err)
	}
	return owner
}

type countedText struct {
	calls int
	value string
}

func (source *countedText) ResolveText(context.Context, systemprompt.AssembleContext) (string, error) {
	source.calls++
	return source.value, nil
}

type countedVariable struct {
	calls int
	value string
}

func (source *countedVariable) ResolveVariable(context.Context, systemprompt.AssembleContext) (systemprompt.VariableValue, error) {
	source.calls++
	return systemprompt.VariableValue{
		Value:   source.value,
		Defined: true,
	}, nil
}

func boolPointer(selected bool) *bool {
	return &selected
}

func TestSystemPromptAssemblesBuiltinsProvidersAndTools(t *testing.T) {
	t.Parallel()
	prompts := newPrompts(
		t,
		systemprompt.Config{
			Persona: "Mode: {{mode}}.",
			ToolOrder: []string{
				"todo",
				systemprompt.ToolOrderRest,
				"bash",
			},
		},
	)
	requestContext := context.Background()
	if _, err := prompts.AddSection(requestContext, systemprompt.PromptSection{
		Name:  "rules",
		Order: 10,
		Text:  systemprompt.StaticText("Be precise."),
	}); err != nil {
		t.Fatal(err)
	}
	dynamic := &countedText{
		value: "cwd: /tmp",
	}
	if _, err := prompts.AddSection(requestContext, systemprompt.PromptSection{
		Name:  "cwd",
		Order: 20,
		Text:  dynamic,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := prompts.AddContext(requestContext, systemprompt.PromptContext{
		Name:  "policy",
		Order: 1,
		Text:  systemprompt.StaticText("context {{mode}}"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := prompts.AddVariable(
		requestContext,
		"mode",
		&countedVariable{
			value: "strict",
		},
	); err != nil {
		t.Fatal(err)
	}
	parameters := json.RawMessage(`{"type":"object"}`)
	input := systemprompt.AssembleContext{
		Tools: systemprompt.ToolDefinitions{
			Schemas: []llm.ToolSchema{
				{
					Name:        "bash",
					Description: "shell",
					Parameters:  parameters,
				},
				{
					Name:       "zeta",
					Parameters: json.RawMessage(`{}`),
				},
				{
					Name:       "todo",
					Parameters: json.RawMessage(`{}`),
				},
				{
					Name:       "alpha",
					Parameters: json.RawMessage(`{}`),
				},
			},
		},
	}

	assembled, err := prompts.Assemble(
		requestContext,
		input,
	)
	if err != nil {
		t.Fatal(err)
	}
	if dynamic.calls != 1 {
		t.Fatalf("dynamic provider calls = %d", dynamic.calls)
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
	rootPrompts := newPrompts(
		t,
		systemprompt.Config{
			Persona: "global persona",
		},
	)
	requestContext := context.Background()
	globalTextSource := &countedText{
		value: "global shared",
	}
	if _, err := rootPrompts.AddSection(requestContext, systemprompt.PromptSection{
		Name:  "shared",
		Order: 5,
		Text:  globalTextSource,
	}); err != nil {
		t.Fatal(err)
	}
	globalVariable := &countedVariable{
		value: "global",
	}
	if _, err := rootPrompts.AddVariable(
		requestContext,
		"mode",
		globalVariable,
	); err != nil {
		t.Fatal(err)
	}
	overlay := systemprompt.DeriveLayer(rootPrompts.Capture())
	if _, err := overlay.AddSection(requestContext, systemprompt.PromptSection{
		Name:  systemprompt.PersonaSection,
		Order: systemprompt.PersonaOrder,
		Text:  systemprompt.StaticText("scoped persona {{mode}}"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := overlay.AddSection(requestContext, systemprompt.PromptSection{
		Name:  "shared",
		Order: 5,
		Text:  systemprompt.StaticText("scoped shared"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := overlay.AddVariable(
		requestContext,
		"mode",
		&countedVariable{
			value: "strict",
		},
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
	if globalTextSource.calls != 0 {
		t.Fatalf("shadowed global section provider calls = %d", globalTextSource.calls)
	}
	if globalVariable.calls != 1 {
		t.Fatalf("ancestor variable provider calls = %d", globalVariable.calls)
	}
	if err := overlay.Close(requestContext); err != nil {
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

func TestCompleteSectionReplacesOrdinarySections(t *testing.T) {
	prompts := newPrompts(t, systemprompt.Config{})
	if _, err := prompts.AddSection(context.Background(), systemprompt.PromptSection{
		Name:     "complete",
		Order:    50,
		Text:     systemprompt.StaticText("complete prompt"),
		Complete: true,
	}); err != nil {
		t.Fatal(err)
	}
	assembled, err := prompts.Assemble(context.Background(), systemprompt.AssembleContext{})
	if err != nil {
		t.Fatal(err)
	}
	want := []systemprompt.AssembledSection{
		{
			Name: "complete",
			Text: "complete prompt",
		},
	}
	if !reflect.DeepEqual(assembled.Sections, want) {
		t.Fatalf("complete sections = %#v", assembled.Sections)
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
	prompts := newPrompts(
		t,
		systemprompt.Config{},
	)
	if _, err := prompts.AddSection(
		context.Background(),
		systemprompt.PromptSection{
			Name:  "nan",
			Order: math.NaN(),
			Text:  systemprompt.StaticText("x"),
		},
	); err == nil || !strings.Contains(err.Error(), "finite") {
		t.Fatalf("non-finite section error = %v", err)
	}
	if _, err := prompts.AddVariable(
		context.Background(),
		"Bad-Name",
		&countedVariable{},
	); err == nil || !strings.Contains(err.Error(), "invalid prompt variable") {
		t.Fatalf("variable name error = %v", err)
	}
	if _, err := prompts.Assemble(context.Background(), systemprompt.AssembleContext{
		Tools: systemprompt.ToolDefinitions{
			Schemas: []llm.ToolSchema{
				{
					Name:       systemprompt.ToolOrderRest,
					Parameters: json.RawMessage(`{}`),
				},
			},
		},
	}); err == nil || !strings.Contains(err.Error(), "reserved tool name") {
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
	prompts := newPrompts(
		t,
		systemprompt.Config{
			IncludeHarnessIdentity: boolPointer(false),
			IncludeRuntimeContext:  boolPointer(false),
			Persona:                "deployment",
		},
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
