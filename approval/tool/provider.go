// Package approvaltool adapts approval business operations to the common ToolActor.
package approvaltool

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/approval"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/tools"
	"github.com/gorenx/goren/tools/tool"
)

// Log is a Session selected once for this invocation. Subsequent operations must
// retain that lifetime, even if its reusable SessionID is activated again.
type Log interface {
	approval.Journal
	Snapshot(context.Context) (session.Snapshot, error)
}

// Sessions selects the live Session without transferring ownership to the tool.
type Sessions interface {
	Open(context.Context, session.SessionID) (Log, error)
}

// Provider binds immutable approval rules, Session access and the human answerer.
// It does not own the borrowed capabilities or a separate approval Actor.
type Provider struct {
	rules   *approval.Service
	logs    Sessions
	answers approval.Answerer
}

// NewProvider validates complete dependencies before any tool is admitted.
func NewProvider(settings approval.ValidatedConfig, logs Sessions, answers approval.Answerer) (*Provider, error) {
	if logs == nil || answers == nil {
		return nil, errors.New("approval tool: Session access and answerer are required")
	}
	rules, err := approval.New(settings)
	if err != nil {
		return nil, err
	}
	return &Provider{
		rules:   rules,
		logs:    logs,
		answers: answers,
	}, nil
}

var parameters = json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"toolName":{"type":"string","minLength":1},"callId":{"type":"string","minLength":1},"reason":{"type":"string"}},"required":["toolName","callId","reason"]}`)
var output = json.RawMessage(`{"type":"string","enum":["allowed-once","rejected","cancelled","unavailable"]}`)

// Schema describes the internal named approval tool. Composition hides it from
// model definitions while retaining it for policy-directed Runtime invocation.
func Schema() llm.ToolSchema {
	return llm.ToolSchema{
		Name:        approval.ServiceName,
		Description: "Request permission for one tool call.",
		Parameters:  append(json.RawMessage(nil), parameters...),
	}
}

// Open creates one tool body borrowing process capabilities.
func (source *Provider) Open(ctx context.Context) (tool.Implementation, error) {
	if ctx == nil {
		return tool.Implementation{}, errors.New("approval tool: context is required")
	}
	err := ctx.Err()
	if err != nil {
		return tool.Implementation{}, err
	}
	definition := Schema()
	body := &executor{
		rules:   source.rules,
		logs:    source.logs,
		answers: source.answers,
	}
	return tool.Implementation{
		Definition: tools.ToolDefinition{
			Name:        definition.Name,
			Description: definition.Description,
			Parameters:  definition.Parameters,
			Output: tools.ToolOutputDefinition{
				Schema:   append(json.RawMessage(nil), output...),
				Renderer: body,
			},
			Executor: body,
		},
	}, nil
}

type executor struct {
	rules   *approval.Service
	logs    Sessions
	answers approval.Answerer
}

func (*executor) Render(_ json.RawMessage, value json.RawMessage) ([]agentmessage.ContentBlock, error) {
	var answer approval.Outcome
	err := json.Unmarshal(value, &answer)
	if err != nil {
		return nil, err
	}
	return []agentmessage.ContentBlock{agentmessage.NewTextBlock(string(answer))}, nil
}

func (body *executor) Execute(arguments json.RawMessage, scope tools.ToolRunContext) (json.RawMessage, error) {
	var input approval.Permission
	err := json.Unmarshal(arguments, &input)
	if err != nil {
		return nil, err
	}
	selected, err := body.logs.Open(scope.Context, scope.Execution.SessionID)
	if err != nil {
		return nil, err
	}
	history, err := selected.Snapshot(scope.Context)
	if err != nil {
		return nil, err
	}
	work, err := body.rules.Begin(approval.Request{
		SessionID: scope.Execution.SessionID,
		Events:    history.Events,
		ToolName:  input.ToolName,
		CallID:    &input.CallID,
		Reason:    &input.Reason,
	})
	if err != nil {
		return nil, err
	}
	answer, err := work.Run(scope.Context, selected, body.answers)
	if err != nil {
		return nil, err
	}
	return json.Marshal(answer)
}

var _ tool.Provider = (*Provider)(nil)
