package inprocess

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
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

type structuredCapture struct {
	plugin.Base
	mutex        sync.Mutex
	schema       json.RawMessage
	staged       map[tools.ToolExecutionToken]json.RawMessage
	captured     json.RawMessage
	toolHandle   *tools.ToolHandle
	promptHandle *systemprompt.PromptHandle
	guardHandle  *tools.GuardHandle
}

func newStructuredCapture(schema json.RawMessage) *structuredCapture {
	return &structuredCapture{
		schema: append(json.RawMessage(nil), schema...),
		staged: make(map[tools.ToolExecutionToken]json.RawMessage),
	}
}

func (*structuredCapture) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "@goren/subagent/structured-output",
		Requires: []plugin.ServiceType{
			plugin.ServiceOf[tools.ToolCatalog](),
			plugin.ServiceOf[tools.PolicyRegistry](),
			plugin.ServiceOf[systemprompt.PromptRegistry](),
		},
		Events: []plugin.EventSubscription{
			plugin.EventOf[tools.ExecutionCompleted](),
		},
	}
}

func (capture *structuredCapture) Apply(requestContext context.Context) error {
	catalog, requireErr := plugin.Require[tools.ToolCatalog](capture)
	if requireErr != nil {
		return requireErr
	}
	policies, requireErr := plugin.Require[tools.PolicyRegistry](capture)
	if requireErr != nil {
		return requireErr
	}
	prompts, requireErr := plugin.Require[systemprompt.PromptRegistry](capture)
	if requireErr != nil {
		return requireErr
	}
	toolHandle, addErr := catalog.AddTool(
		requestContext,
		tools.ToolDefinition{
			Name:        structuredOutputTool,
			Description: "Report your final structured result. Call this exactly once, when your answer is complete; the arguments must match this tool's parameter schema exactly.",
			Parameters:  capture.schema,
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
			Executor: tools.ExecutorFunc(capture.execute),
		},
	)
	if addErr != nil {
		return addErr
	}
	capture.toolHandle = toolHandle
	promptHandle, addErr := prompts.AddSection(
		requestContext,
		systemprompt.PromptSection{
			Name:  structuredPromptName,
			Order: 190,
			Text:  systemprompt.StaticText(structuredPromptText),
		},
	)
	if addErr != nil {
		return errors.Join(addErr, capture.release(context.WithoutCancel(requestContext)))
	}
	capture.promptHandle = promptHandle
	guardHandle, addErr := policies.AddGuard(
		requestContext,
		structuredOutputTool,
		tools.ToolGuardFunc(capture.denyAfterCapture),
	)
	if addErr != nil {
		return errors.Join(addErr, capture.release(context.WithoutCancel(requestContext)))
	}
	capture.guardHandle = guardHandle
	return nil
}

func (capture *structuredCapture) execute(
	arguments json.RawMessage,
	runContext tools.ToolRunContext,
) (json.RawMessage, error) {
	capture.mutex.Lock()
	capture.staged[runContext.Execution.Token] = append(
		json.RawMessage(nil),
		arguments...,
	)
	capture.mutex.Unlock()
	runContext.ConcludeTurn()
	return json.RawMessage(`{"recorded":true}`), nil
}

func (capture *structuredCapture) denyAfterCapture(
	tools.ToolExecution,
) (string, bool) {
	capture.mutex.Lock()
	defer capture.mutex.Unlock()
	if capture.captured == nil && len(capture.staged) == 0 {
		return "", false
	}
	return "structured output already recorded: the run is complete", true
}

func (capture *structuredCapture) ObserveEvent(
	_ context.Context,
	fact plugin.Event,
) error {
	completed, matches := fact.(tools.ExecutionCompleted)
	if !matches || completed.Execution().Name != structuredOutputTool {
		return nil
	}
	execution := completed.Execution()
	capture.mutex.Lock()
	staged, found := capture.staged[execution.Token]
	if found {
		delete(capture.staged, execution.Token)
	}
	if found && !completed.Result().Failed() &&
		execution.Parent.IsZero() && capture.captured == nil {
		capture.captured = append(json.RawMessage(nil), staged...)
	}
	capture.mutex.Unlock()
	return nil
}

func (capture *structuredCapture) Captured() json.RawMessage {
	if capture == nil {
		return nil
	}
	capture.mutex.Lock()
	defer capture.mutex.Unlock()
	return append(json.RawMessage(nil), capture.captured...)
}

func (capture *structuredCapture) Dispose(closeContext context.Context) error {
	return capture.release(closeContext)
}

func (capture *structuredCapture) release(closeContext context.Context) error {
	if closeContext == nil {
		closeContext = context.Background()
	}
	var releaseErr error
	if capture.guardHandle != nil {
		releaseErr = errors.Join(
			releaseErr,
			capture.guardHandle.Unregister(closeContext),
		)
		capture.guardHandle = nil
	}
	if capture.promptHandle != nil {
		releaseErr = errors.Join(
			releaseErr,
			capture.promptHandle.Unregister(closeContext),
		)
		capture.promptHandle = nil
	}
	if capture.toolHandle != nil {
		releaseErr = errors.Join(
			releaseErr,
			capture.toolHandle.Unregister(closeContext),
		)
		capture.toolHandle = nil
	}
	return releaseErr
}

var _ plugin.EventObserver = (*structuredCapture)(nil)
