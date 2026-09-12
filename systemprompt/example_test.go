package systemprompt_test

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/systemprompt"
)

// exampleToolSummary computes context text from this assembly's input values.
// It holds no Runtime, Session or registration owner.
type exampleToolSummary struct{}

func (exampleToolSummary) ResolveText(_ context.Context, input systemprompt.AssembleContext) (string, error) {
	return fmt.Sprintf("Available tools: %d", len(input.Tools.Schemas)), nil
}

func ExamplePromptLayer_Assemble() {
	requestContext := context.Background()
	includeIdentity := false
	validated, err := systemprompt.ValidateConfig(systemprompt.Config{
		IncludeHarnessIdentity: &includeIdentity,
	})
	if err != nil {
		panic(err)
	}
	basePrompts, err := systemprompt.NewLayer(validated)
	if err != nil {
		panic(err)
	}
	defer basePrompts.Close(requestContext)
	prompts := systemprompt.DeriveLayer(basePrompts.Capture())
	defer prompts.Close(requestContext)

	// Register definitions: one instruction and one computed runtime fact.
	// Reusing PersonaSection in the child shadows the inherited persona.
	if _, err := prompts.AddSection(requestContext, systemprompt.PromptSection{
		Name:  systemprompt.PersonaSection,
		Order: systemprompt.PersonaOrder,
		Text:  systemprompt.StaticText("Answer in {{language}}."),
	}); err != nil {
		panic(err)
	}
	if _, err := prompts.AddContext(requestContext, systemprompt.PromptContext{
		Name:  "tools",
		Order: 10,
		Text:  exampleToolSummary{},
	}); err != nil {
		panic(err)
	}
	if _, err := prompts.AddVariable(requestContext, "language", systemprompt.VariableValue{
		Value:   "Chinese",
		Defined: true,
	}); err != nil {
		panic(err)
	}

	// This call input supplies data to providers; it registers no new tools.
	// Production Runtime supplies its already-consumed tool-definition view.
	assembled, err := prompts.Capture().Assemble(requestContext, systemprompt.AssembleContext{
		Tools: systemprompt.ToolDefinitions{
			Schemas: []llm.ToolSchema{
				{
					Name:       "read_file",
					Parameters: json.RawMessage(`{"type":"object"}`),
				},
			},
		},
	})
	if err != nil {
		panic(err)
	}

	// These are AssembledSection and AssembledContext values. Providers have
	// resolved, but variable interpolation is a separate rendering step.
	fmt.Println("section:", assembled.Sections[0].Text)
	fmt.Println("context:", assembled.Contexts[0].Text)
	promptText, err := systemprompt.RenderPrompt(assembled)
	if err != nil {
		panic(err)
	}
	contextSections, err := systemprompt.RenderContextSections(assembled)
	if err != nil {
		panic(err)
	}
	fmt.Println("system prompt:", promptText)
	fmt.Println("runtime context:", contextSections[0].Text)

	// Output:
	// section: Answer in {{language}}.
	// context: Available tools: 1
	// system prompt: Answer in Chinese.
	// runtime context: Available tools: 1
}
