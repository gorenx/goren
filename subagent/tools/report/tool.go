package report

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/gorenx/goren/agentmessage"
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

type reporter interface {
	Report(
		context.Context,
		session.SessionID,
		string,
		Delivery,
	) (agentmessage.MessageID, error)
}

// reportTool translates one model Tool call into a direct parent Agent Inbox
// operation. It has no Plugin registration lifecycle.
type reportTool struct {
	reporter   reporter
	scheduling Delivery
}

func newReportTool(
	reporting reporter,
	selectedDelivery Delivery,
) (*reportTool, error) {
	if reporting == nil {
		return nil, errors.New("subagent report: Reporter is required")
	}
	return &reportTool{
		reporter:   reporting,
		scheduling: selectedDelivery,
	}, nil
}

func (adapter *reportTool) definition() tools.ToolDefinition {
	return tools.ToolDefinition{
		Name:        "report",
		Description: "Report selected self-contained content to the agent that started you. Reporting does not finish your work and only your direct parent receives it.",
		Parameters:  reportParameters,
		Output: tools.ToolOutputDefinition{
			Schema:   reportOutput,
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
	messageID, messageErr := adapter.reporter.Report(
		runContext.Context,
		childID,
		request.Output,
		adapter.scheduling,
	)
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
