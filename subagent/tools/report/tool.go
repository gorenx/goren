package report

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/tools"
)

const description = "Report selected self-contained content to the agent that started you. Reporting does not finish your work and only your direct parent receives it."

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

// Report contains the caller's selected output and the configured parent
// scheduling policy. The recipient is always derived from the direct relationship.
type Report struct {
	ChildSessionID session.SessionID
	Output         string
	Delivery       Delivery
}

// Reporter delivers selected content to the caller's direct parent. The context
// carries AgentCreationID for exact lifetime checks; accepting a report does not
// complete the child's turn or imply that the parent has consumed it.
type Reporter interface {
	Report(context.Context, Report) (agentmessage.MessageID, error)
}

// reportTool translates one model Tool call into a direct parent Agent Inbox
// operation. It has no Plugin registration lifecycle.
type reportTool struct {
	reporting  Reporter
	scheduling Delivery
}

func newReportTool(
	reporting Reporter,
	selectedDelivery Delivery,
) (*reportTool, error) {
	if reporting == nil {
		return nil, errors.New("subagent report: Reporter is required")
	}
	if selectedDelivery != Quiet && selectedDelivery != NextStep {
		return nil, errors.New("subagent report: unsupported delivery")
	}
	return &reportTool{
		reporting:  reporting,
		scheduling: selectedDelivery,
	}, nil
}

func (adapter *reportTool) definition() tools.ToolDefinition {
	return tools.ToolDefinition{
		Name:        "report",
		Description: description,
		Parameters:  append(json.RawMessage(nil), reportParameters...),
		Output: tools.ToolOutputDefinition{
			Schema:   append(json.RawMessage(nil), reportOutput...),
			Renderer: adapter,
		},
		Executor: adapter,
	}
}

func (adapter *reportTool) Execute(
	rawArguments json.RawMessage,
	runContext tools.ToolRunContext,
) (json.RawMessage, error) {
	if runContext.Context == nil {
		return nil, errors.New("subagent report: Tool context is nil")
	}
	if contextErr := runContext.Context.Err(); contextErr != nil {
		return nil, contextErr
	}
	childID := runContext.Execution.SessionID
	if childID == "" {
		return nil, errors.New("report requires a calling child Agent")
	}
	var request reportArguments
	if decodeErr := json.Unmarshal(rawArguments, &request); decodeErr != nil {
		return nil, decodeErr
	}
	messageID, messageErr := adapter.reporting.Report(runContext.Context, Report{
		ChildSessionID: childID,
		Output:         request.Output,
		Delivery:       adapter.scheduling,
	})
	if messageErr != nil {
		return nil, messageErr
	}
	return json.Marshal(struct {
		MessageID string `json:"messageId"`
	}{
		MessageID: string(messageID),
	})
}

func (adapter *reportTool) Render(
	_ json.RawMessage,
	rawValue json.RawMessage,
) ([]agentmessage.ContentBlock, error) {
	var value struct {
		MessageID string `json:"messageId"`
	}
	if decodeErr := json.Unmarshal(rawValue, &value); decodeErr != nil {
		return nil, decodeErr
	}
	return []agentmessage.ContentBlock{
		agentmessage.NewTextBlock(
			"report accepted by the agent that started you as message " + value.MessageID,
		),
	}, nil
}
