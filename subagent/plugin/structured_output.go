package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/gorenx/goren/llm"
	pluginruntime "github.com/gorenx/goren/plugin"
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
	pluginruntime.Base
	mutex        sync.Mutex
	schema       json.RawMessage
	staged       map[tools.ToolExecutionToken]json.RawMessage
	captured     json.RawMessage
	toolHandle   *tools.ToolHandle
	promptHandle *systemprompt.PromptHandle
	guardHandle  *tools.GuardHandle
}

func newStructuredOutput(schema json.RawMessage) *structuredOutput {
	return &structuredOutput{
		schema: append(json.RawMessage(nil), schema...),
		staged: make(map[tools.ToolExecutionToken]json.RawMessage),
	}
}

func (*structuredOutput) Manifest() pluginruntime.Manifest {
	return pluginruntime.Manifest{
		Name: "@goren/subagent/structured-output",
		Requires: []pluginruntime.ServiceType{
			pluginruntime.ServiceOf[tools.ToolCatalog](),
			pluginruntime.ServiceOf[tools.PolicyRegistry](),
			pluginruntime.ServiceOf[systemprompt.PromptRegistry](),
		},
		Events: []pluginruntime.EventSubscription{
			pluginruntime.EventOf[tools.ExecutionCompleted](),
		},
	}
}

func (output *structuredOutput) Apply(requestContext context.Context) error {
	catalog, requireErr := pluginruntime.Require[tools.ToolCatalog](output)
	if requireErr != nil {
		return requireErr
	}
	policies, requireErr := pluginruntime.Require[tools.PolicyRegistry](output)
	if requireErr != nil {
		return requireErr
	}
	prompts, requireErr := pluginruntime.Require[systemprompt.PromptRegistry](output)
	if requireErr != nil {
		return requireErr
	}
	toolHandle, addErr := catalog.AddTool(
		requestContext,
		tools.ToolDefinition{
			Name:        structuredOutputTool,
			Description: "Report the final structured result exactly once.",
			Parameters:  output.schema,
			Output: tools.ToolOutputDefinition{
				Schema: structuredResultSchema,
				Renderer: tools.OutputRendererFunc(func(
					json.RawMessage,
					json.RawMessage,
				) ([]llm.ContentBlock, error) {
					return []llm.ContentBlock{
						llm.NewTextBlock("Structured output recorded."),
					}, nil
				}),
			},
			Executor: tools.ExecutorFunc(output.execute),
		},
	)
	if addErr != nil {
		return addErr
	}
	output.toolHandle = toolHandle
	promptHandle, addErr := prompts.AddSection(
		requestContext,
		systemprompt.PromptSection{
			Name:  structuredPromptName,
			Order: 190,
			Text:  systemprompt.StaticText(structuredPromptText),
		},
	)
	if addErr != nil {
		return errors.Join(
			addErr,
			output.release(context.WithoutCancel(requestContext)),
		)
	}
	output.promptHandle = promptHandle
	guardHandle, addErr := policies.AddGuard(
		requestContext,
		structuredOutputTool,
		tools.ToolGuardFunc(output.denyAfterCapture),
	)
	if addErr != nil {
		return errors.Join(
			addErr,
			output.release(context.WithoutCancel(requestContext)),
		)
	}
	output.guardHandle = guardHandle
	return nil
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

func (output *structuredOutput) ObserveEvent(
	_ context.Context,
	fact pluginruntime.Event,
) error {
	completed, matches := fact.(tools.ExecutionCompleted)
	if !matches || completed.Execution().Name != structuredOutputTool {
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

func (output *structuredOutput) Dispose(closeContext context.Context) error {
	return output.release(closeContext)
}

func (output *structuredOutput) release(closeContext context.Context) error {
	if closeContext == nil {
		closeContext = context.Background()
	}
	var releaseErr error
	if output.guardHandle != nil {
		releaseErr = errors.Join(
			releaseErr,
			output.guardHandle.Unregister(closeContext),
		)
		output.guardHandle = nil
	}
	if output.promptHandle != nil {
		releaseErr = errors.Join(
			releaseErr,
			output.promptHandle.Unregister(closeContext),
		)
		output.promptHandle = nil
	}
	if output.toolHandle != nil {
		releaseErr = errors.Join(
			releaseErr,
			output.toolHandle.Unregister(closeContext),
		)
		output.toolHandle = nil
	}
	return releaseErr
}

var _ pluginruntime.EventObserver = (*structuredOutput)(nil)
