package control

import (
	"encoding/json"
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/tools"
)

var interruptParameters = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "agent_id": {"type": "string"}
  },
  "required": ["agent_id"]
}`)

var acceptedOutput = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "accepted": {"type": "boolean", "const": true}
  },
  "required": ["accepted"]
}`)

type interruptArguments struct {
	AgentID string `json:"agent_id"`
}

func (adapter *controlTools) interruptDefinition() tools.ToolDefinition {
	return tools.ToolDefinition{
		Name:        "interrupt_agent",
		Description: "Request cancellation of a descendant background agent's current turn. The request returns immediately; queued messages and the resident agent remain available.",
		Parameters:  interruptParameters,
		Output: tools.ToolOutputDefinition{
			Schema: acceptedOutput,
			Renderer: tools.OutputRendererFunc(func(
				rawArguments json.RawMessage,
				_ json.RawMessage,
			) ([]agentmessage.ContentBlock, error) {
				var request interruptArguments
				if decodeErr := json.Unmarshal(rawArguments, &request); decodeErr != nil {
					return nil, decodeErr
				}
				return []agentmessage.ContentBlock{
					agentmessage.NewTextBlock(
						"interrupt requested for agent " + request.AgentID,
					),
				}, nil
			}),
		},
		Executor: tools.ExecutorFunc(adapter.interrupt),
	}
}

func (adapter *controlTools) interrupt(
	rawArguments json.RawMessage,
	runContext tools.ToolRunContext,
) (json.RawMessage, error) {
	callerAgent, matches := runContext.Execution.Subject.(agent.Agent)
	if !matches || callerAgent == nil {
		return nil, errors.New("interrupt_agent requires a calling Agent")
	}
	var request interruptArguments
	if decodeErr := json.Unmarshal(rawArguments, &request); decodeErr != nil {
		return nil, decodeErr
	}
	interruptErr := adapter.children.Interrupt(
		runContext.Context,
		session.SessionID(request.AgentID),
		subagent.AncestorInterruptAuthority{
			Agent: callerAgent,
		},
	)
	if interruptErr != nil {
		return nil, interruptErr
	}
	return json.RawMessage(`{"accepted":true}`), nil
}
