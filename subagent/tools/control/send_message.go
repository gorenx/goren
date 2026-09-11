package control

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/tools"
)

// Message contains a parent-authored task for one existing child. Caller identity
// comes from the trusted invocation; model parameters select only child and text.
type Message struct {
	ParentSessionID session.SessionID
	ChildSessionID  session.SessionID
	Content         []agentmessage.ContentBlock
}

// Sender delivers ordinary input to an authorized child. The execution context
// carries AgentCreationID for checking the caller's exact lifetime.
type Sender interface {
	Send(context.Context, Message) (agentmessage.MessageID, error)
}

const messageDescription = "Send a message to a background subagent by its durable id. The message becomes its next turn and this call returns only delivery confirmation."

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

func (owner *controlTools) sendMessageDefinition() tools.ToolDefinition {
	adapter := &messageTool{
		children: owner.messages,
	}
	return adapter.definition()
}

func (adapter *messageTool) definition() tools.ToolDefinition {
	return tools.ToolDefinition{
		Name:        "send_message",
		Description: messageDescription,
		Parameters:  append(json.RawMessage(nil), sendMessageParameters...),
		Output: tools.ToolOutputDefinition{
			Schema:   append(json.RawMessage(nil), messageIDOutput...),
			Renderer: adapter,
		},
		Executor: adapter,
	}
}

func (adapter *messageTool) Execute(
	rawArguments json.RawMessage,
	runContext tools.ToolRunContext,
) (json.RawMessage, error) {
	if runContext.Context == nil {
		return nil, errors.New("send_message requires an execution context")
	}
	if err := runContext.Context.Err(); err != nil {
		return nil, err
	}
	parentID := runContext.Execution.SessionID
	if parentID == "" {
		return nil, errors.New("send_message requires a calling Agent")
	}
	var request sendMessageArguments
	if decodeErr := json.Unmarshal(rawArguments, &request); decodeErr != nil {
		return nil, decodeErr
	}
	messageID, followErr := adapter.children.Send(runContext.Context, Message{
		ParentSessionID: parentID,
		ChildSessionID:  session.SessionID(request.SubagentID),
		Content: []agentmessage.ContentBlock{
			agentmessage.NewTextBlock(request.Message),
		},
	})
	if followErr != nil {
		return nil, followErr
	}
	return json.Marshal(struct {
		MessageID string `json:"messageId"`
	}{
		MessageID: string(messageID),
	})
}

func (adapter *messageTool) Render(
	rawArguments json.RawMessage,
	_ json.RawMessage,
) ([]agentmessage.ContentBlock, error) {
	var request sendMessageArguments
	if decodeErr := json.Unmarshal(rawArguments, &request); decodeErr != nil {
		return nil, decodeErr
	}
	return []agentmessage.ContentBlock{
		agentmessage.NewTextBlock(
			"message queued as the next turn for subagent " + request.SubagentID,
		),
	}, nil
}

type messageTool struct {
	children Sender
}
