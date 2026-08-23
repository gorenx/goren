package report

import (
	"encoding/json"
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/tools"
)

var reportParameters = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "output": {"type": "string"}
  },
  "required": ["output"]
}`)

var reportOutput = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "messageId": {"type": "string"}
  },
  "required": ["messageId"]
}`)

type reportArguments struct {
	Output string `json:"output"`
}

func (child *childPlugin) definition() tools.ToolDefinition {
	return tools.ToolDefinition{
		Name:        "report",
		Description: "Report selected self-contained content to the agent that started you. Reporting does not finish your work and only your direct parent receives it.",
		Parameters:  reportParameters,
		Output: tools.ToolOutputDefinition{
			Schema: reportOutput,
			Renderer: tools.OutputRendererFunc(func(
				_ json.RawMessage,
				rawValue json.RawMessage,
			) ([]llm.ContentBlock, error) {
				var value struct {
					MessageID string `json:"messageId"`
				}
				if decodeErr := json.Unmarshal(rawValue, &value); decodeErr != nil {
					return nil, decodeErr
				}
				return []llm.ContentBlock{
					llm.NewTextBlock(
						"report accepted by the agent that started you as message " + value.MessageID,
					),
				}, nil
			}),
		},
		Executor: tools.ExecutorFunc(child.execute),
	}
}

func (child *childPlugin) execute(
	rawArguments json.RawMessage,
	runContext tools.ToolRunContext,
) (json.RawMessage, error) {
	childAgent, matches := runContext.Execution.Subject.(agent.Agent)
	if !matches || childAgent == nil {
		return nil, errors.New("report requires a calling continuable Agent")
	}
	var request reportArguments
	if decodeErr := json.Unmarshal(rawArguments, &request); decodeErr != nil {
		return nil, decodeErr
	}
	messageID, reportErr := child.continuations.ReportFrom(
		runContext.Context,
		childAgent,
		[]llm.ContentBlock{
			llm.NewTextBlock(request.Output),
		},
		subagent.ReportOptions{
			Delivery: child.delivery,
		},
	)
	if reportErr != nil {
		return nil, reportErr
	}
	return json.Marshal(struct {
		MessageID string `json:"messageId"`
	}{
		MessageID: string(messageID),
	})
}
