package report

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
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

// liveAgents is the report use case's read-only view of resident Agents.
type liveAgents interface {
	Get(session.SessionID) (agent.Agent, bool)
	Contains(agent.Agent) bool
}

// reportTool translates one model Tool call into a direct parent Agent Inbox
// operation. It has no Plugin registration lifecycle.
type reportTool struct {
	agents     liveAgents
	scheduling Delivery
}

func newReportTool(
	agents liveAgents,
	selectedDelivery Delivery,
) (*reportTool, error) {
	if agents == nil {
		return nil, errors.New("subagent report: Agent Registry is required")
	}
	return &reportTool{
		agents:     agents,
		scheduling: selectedDelivery,
	}, nil
}

func (adapter *reportTool) definition() tools.ToolDefinition {
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
		Executor: tools.ExecutorFunc(adapter.execute),
	}
}

func (adapter *reportTool) execute(
	rawArguments json.RawMessage,
	runContext tools.ToolRunContext,
) (json.RawMessage, error) {
	if runContext.Context == nil {
		return nil, errors.New("subagent report: Tool context is nil")
	}
	if contextErr := runContext.Context.Err(); contextErr != nil {
		return nil, contextErr
	}
	childAgent, matches := runContext.Execution.Subject.(agent.Agent)
	if !matches || childAgent == nil {
		return nil, errors.New("report requires a calling child Agent")
	}
	if !adapter.agents.Contains(childAgent) {
		return nil, &subagent.Error{
			Code:    subagent.ErrorUnauthorized,
			Message: "report requires the exact live child Agent",
		}
	}
	var request reportArguments
	if decodeErr := json.Unmarshal(rawArguments, &request); decodeErr != nil {
		return nil, decodeErr
	}
	childSession := childAgent.SessionValue()
	if childSession == nil {
		return nil, &subagent.Error{
			Code:    subagent.ErrorUnauthorized,
			Message: "report requires a child Agent with a direct parent",
		}
	}
	childHeader := childSession.Header()
	if childHeader.ParentSession == nil {
		return nil, &subagent.Error{
			Code:    subagent.ErrorUnauthorized,
			Message: "report requires a child Agent with a direct parent",
		}
	}
	parentID := *childHeader.ParentSession
	parentAgent, found := adapter.agents.Get(parentID)
	if !found {
		return nil, &subagent.Error{
			Code:    subagent.ErrorParentUnavailable,
			Message: "direct parent is not live; report was not delivered",
		}
	}
	messageValue, messageErr := llm.NewUserMessage(llm.UserMessageInput{
		Content: []llm.ContentBlock{
			llm.NewTextBlock(
				fmt.Sprintf("Subagent %s reported:", childAgent.ID()),
			),
			llm.NewTextBlock(request.Output),
		},
		Source: subagent.ReportSource{
			SenderSessionID: childAgent.ID(),
		},
	})
	if messageErr != nil {
		return nil, messageErr
	}
	switch adapter.scheduling {
	case Quiet:
		messageErr = parentAgent.Inject(messageValue)
	case NextStep:
		messageErr = parentAgent.Steer(messageValue)
	default:
		return nil, errors.New("subagent report: unsupported delivery")
	}
	if messageErr != nil {
		return nil, &subagent.Error{
			Code:    subagent.ErrorParentUnavailable,
			Message: "direct parent is not live; report was not delivered",
			Cause:   messageErr,
		}
	}
	return json.Marshal(struct {
		MessageID string `json:"messageId"`
	}{
		MessageID: string(messageValue.StableID()),
	})
}
