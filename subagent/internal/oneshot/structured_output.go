package oneshot

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/systemprompt"
	"github.com/gorenx/goren/tools"
)

const (
	structuredOutputTool = "structured_output"
	structuredPromptName = "tool:structured_output"
	structuredPromptText = "When you have your final answer, you MUST report it by calling the `structured_output` tool with arguments matching its parameter schema exactly. Do not finish with a plain text answer: only the tool call counts as your result."
)

var structuredResultSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "recorded": {
      "type": "boolean",
      "const": true
    }
  },
  "required": ["recorded"],
  "additionalProperties": false
}`)

// structuredOutput owns the result Tool, prompt, guard, and captured value
// installed into one exact OneShot child Scope.
type structuredOutput struct {
	mutex    sync.Mutex
	schema   json.RawMessage
	staged   map[tools.ToolExecutionToken]json.RawMessage
	captured json.RawMessage
}

func newStructuredOutput(schema json.RawMessage) *structuredOutput {
	return &structuredOutput{
		schema: append(json.RawMessage(nil), schema...),
		staged: make(map[tools.ToolExecutionToken]json.RawMessage),
	}
}

func (output *structuredOutput) Apply(
	ctx context.Context,
	_ agent.Agent,
	editor agent.ScopeEditor,
) error {
	if err := editor.AddTool(
		ctx,
		tools.ToolDefinition{
			Name:        structuredOutputTool,
			Description: "Report the final structured result exactly once.",
			Parameters:  output.schema,
			Output: tools.ToolOutputDefinition{
				Schema: structuredResultSchema,
				Renderer: tools.OutputRendererFunc(func(
					json.RawMessage,
					json.RawMessage,
				) ([]agentmessage.ContentBlock, error) {
					return []agentmessage.ContentBlock{
						agentmessage.NewTextBlock("Structured output recorded."),
					}, nil
				}),
			},
			Executor: tools.ExecutorFunc(output.execute),
		},
	); err != nil {
		return err
	}
	if err := editor.AddPromptSection(
		ctx,
		systemprompt.PromptSection{
			Name:  structuredPromptName,
			Order: 190,
			Text:  systemprompt.StaticText(structuredPromptText),
		},
	); err != nil {
		return err
	}
	if err := editor.AddToolGuard(
		ctx,
		structuredOutputTool,
		tools.ToolGuardFunc(output.denyAfterCapture),
	); err != nil {
		return err
	}
	return editor.ObserveToolResults(output)
}

func (output *structuredOutput) execute(
	arguments json.RawMessage,
	runContext tools.ToolRunContext,
) (json.RawMessage, error) {
	output.mutex.Lock()
	output.staged[runContext.Execution.Token] = append(
		json.RawMessage(nil),
		arguments...,
	)
	output.mutex.Unlock()
	runContext.ConcludeTurn()
	return json.RawMessage(`{"recorded":true}`), nil
}

func (output *structuredOutput) denyAfterCapture(
	tools.ToolExecution,
) (string, bool) {
	output.mutex.Lock()
	defer output.mutex.Unlock()
	if output.captured == nil && len(output.staged) == 0 {
		return "", false
	}
	return "structured output already recorded: the execution is complete", true
}

func (output *structuredOutput) ObserveToolResult(
	_ context.Context,
	completed tools.ExecutionCompleted,
) error {
	if completed.Execution().Name != structuredOutputTool {
		return nil
	}
	executionValue := completed.Execution()
	output.mutex.Lock()
	staged, found := output.staged[executionValue.Token]
	if found {
		delete(output.staged, executionValue.Token)
	}
	if found && !completed.Result().Failed() &&
		executionValue.Parent.IsZero() && output.captured == nil {
		output.captured = append(json.RawMessage(nil), staged...)
	}
	output.mutex.Unlock()
	return nil
}

func (output *structuredOutput) Captured() json.RawMessage {
	output.mutex.Lock()
	defer output.mutex.Unlock()
	return append(json.RawMessage(nil), output.captured...)
}

var _ agent.Setup = (*structuredOutput)(nil)
var _ tools.ResultObserver = (*structuredOutput)(nil)
