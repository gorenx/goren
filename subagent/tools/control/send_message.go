package control

import (
	"encoding/json"
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/tools"
)

var sendMessageParameters = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "subagent_id": {"type": "string"},
    "message": {"type": "string"}
  },
  "required": ["subagent_id", "message"]
}`)

var messageIDOutput = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "messageId": {"type": "string"}
  },
  "required": ["messageId"]
}`)

type sendMessageArguments struct {
	SubagentID string `json:"subagent_id"`
	Message    string `json:"message"`
}

func (adapter *controlTools) sendMessageDefinition() tools.ToolDefinition {
	return tools.ToolDefinition{
		Name:        "send_message",
		Description: "Send a message to a background subagent by its durable id. The message becomes its next turn and this call returns only delivery confirmation.",
		Parameters:  sendMessageParameters,
		Output: tools.ToolOutputDefinition{
			Schema: messageIDOutput,
			Renderer: tools.OutputRendererFunc(func(
				rawArguments json.RawMessage,
				_ json.RawMessage,
			) ([]llm.ContentBlock, error) {
				var request sendMessageArguments
				if decodeErr := json.Unmarshal(rawArguments, &request); decodeErr != nil {
					return nil, decodeErr
				}
				return []llm.ContentBlock{
					llm.NewTextBlock(
						"message queued as the next turn for subagent " + request.SubagentID,
					),
				}, nil
			}),
		},
		Executor: tools.ExecutorFunc(adapter.sendMessage),
	}
}

func (adapter *controlTools) sendMessage(
	rawArguments json.RawMessage,
	runContext tools.ToolRunContext,
) (json.RawMessage, error) {
	parentAgent, matches := runContext.Execution.Subject.(agent.Agent)
	if !matches || parentAgent == nil {
		return nil, errors.New("send_message requires a calling Agent")
	}
	var request sendMessageArguments
	if decodeErr := json.Unmarshal(rawArguments, &request); decodeErr != nil {
		return nil, decodeErr
	}
	messageID, followErr := adapter.children.Send(
		runContext.Context,
		parentAgent,
		session.SessionID(request.SubagentID),
		[]llm.ContentBlock{
			llm.NewTextBlock(request.Message),
		},
		subagent.FollowupOptions{
			Source: subagent.CoordinatorSource{
				SenderSessionID: parentAgent.ID(),
			},
		},
	)
	if followErr != nil {
		return nil, followErr
	}
	return json.Marshal(struct {
		MessageID string `json:"messageId"`
	}{
		MessageID: string(messageID),
	})
}
