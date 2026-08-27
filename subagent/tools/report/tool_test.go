package report

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/tools"
)

func TestReportUsesExactChildAndConfiguredDelivery(t *testing.T) {
	t.Parallel()
	adapter, parentAgent, childAgent := newReportFixture(t, Quiet)
	value, executeErr := adapter.execute(
		json.RawMessage(`{"output":"review complete"}`),
		tools.ToolRunContext{
			Context: context.Background(),
			Execution: tools.ToolExecution{
				Subject: childAgent,
			},
		},
	)
	if executeErr != nil {
		t.Fatal(executeErr)
	}
	var result struct {
		MessageID string `json:"messageId"`
	}
	if decodeErr := json.Unmarshal(value, &result); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	parentAgent.mutex.Lock()
	injected := append([]agentmessage.UserMessage(nil), parentAgent.injected...)
	parentAgent.mutex.Unlock()
	if len(injected) != 1 || string(injected[0].StableID()) != result.MessageID {
		t.Fatalf("parent injections = %#v, value=%s", injected, value)
	}
	var content strings.Builder
	for _, block := range injected[0].ContentValue() {
		plain, matches := block.(agentmessage.PlainTextContent)
		if !matches {
			continue
		}
		textValue, visible := plain.PlainText()
		if visible {
			content.WriteString(textValue)
		}
	}
	if !strings.Contains(content.String(), "review complete") {
		t.Fatalf("parent report content = %q", content.String())
	}
	source, matches := injected[0].SourceValue().(subagent.ReportSource)
	if !matches || source.SenderSessionID != childAgent.ID() {
		t.Fatalf("parent report source = %#v", injected[0].SourceValue())
	}
}

func TestReportNextStepSteersParent(t *testing.T) {
	t.Parallel()
	adapter, parentAgent, childAgent := newReportFixture(t, NextStep)
	if _, executeErr := adapter.execute(
		json.RawMessage(`{"output":"act now"}`),
		tools.ToolRunContext{
			Context: context.Background(),
			Execution: tools.ToolExecution{
				Subject: childAgent,
			},
		},
	); executeErr != nil {
		t.Fatal(executeErr)
	}
	parentAgent.mutex.Lock()
	defer parentAgent.mutex.Unlock()
	if len(parentAgent.steered) != 1 || len(parentAgent.injected) != 0 {
		t.Fatalf(
			"parent delivery = steer:%d inject:%d, want 1/0",
			len(parentAgent.steered),
			len(parentAgent.injected),
		)
	}
}

func TestReportDoesNotDeliverAfterCancellation(t *testing.T) {
	t.Parallel()
	adapter, parentAgent, childAgent := newReportFixture(t, Quiet)
	cancelledContext, cancelContext := context.WithCancel(context.Background())
	cancelContext()
	_, executeErr := adapter.execute(
		json.RawMessage(`{"output":"must not be delivered"}`),
		tools.ToolRunContext{
			Context: cancelledContext,
			Execution: tools.ToolExecution{
				Subject: childAgent,
			},
		},
	)
	if !errors.Is(executeErr, context.Canceled) {
		t.Fatalf("report error = %v, want context cancellation", executeErr)
	}
	parentAgent.mutex.Lock()
	defer parentAgent.mutex.Unlock()
	if len(parentAgent.injected) != 0 || len(parentAgent.steered) != 0 {
		t.Fatal("cancelled report reached the parent Agent")
	}
}

func newReportFixture(
	t *testing.T,
	selectedDelivery Delivery,
) (*reportTool, *reportAgent, *reportAgent) {
	t.Helper()
	parentAgent := newReportAgent(t, "parent", nil)
	parentID := parentAgent.ID()
	childAgent := newReportAgent(t, "child", &parentID)
	agents := &reportAgentRegistry{
		entries: map[session.SessionID]agent.Agent{
			parentAgent.ID(): parentAgent,
			childAgent.ID():  childAgent,
		},
	}
	adapter, constructionErr := newReportTool(agents, selectedDelivery)
	if constructionErr != nil {
		t.Fatal(constructionErr)
	}
	return adapter, parentAgent, childAgent
}

type reportAgentRegistry struct {
	entries map[session.SessionID]agent.Agent
}

func (record *reportAgentRegistry) Get(
	identifier session.SessionID,
) (agent.Agent, bool) {
	subject, found := record.entries[identifier]
	return subject, found
}

func (record *reportAgentRegistry) Contains(subject agent.Agent) bool {
	if subject == nil {
		return false
	}
	current, found := record.entries[subject.ID()]
	return found && agent.Same(current, subject)
}

type reportAgent struct {
	plugin.Base
	id       session.SessionID
	session  session.Context
	mutex    sync.Mutex
	injected []agentmessage.UserMessage
	steered  []agentmessage.UserMessage
}

func newReportAgent(
	t *testing.T,
	identifier session.SessionID,
	parentID *session.SessionID,
) *reportAgent {
	t.Helper()
	conversation, err := session.New(
		identifier,
		session.CreateOptions{
			Metadata: session.Metadata{
				ParentSession: parentID,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return &reportAgent{
		id:      identifier,
		session: conversation,
	}
}

func (*reportAgent) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "test/report-agent",
	}
}
func (*reportAgent) Apply(context.Context) error                   { return nil }
func (*reportAgent) Dispose(context.Context) error                 { return nil }
func (subject *reportAgent) ID() session.SessionID                 { return subject.id }
func (*reportAgent) OptionsValue() agent.Options                   { return agent.Options{} }
func (subject *reportAgent) SessionValue() session.Context         { return subject.session }
func (*reportAgent) InboxValue() *agent.Inbox                      { return nil }
func (*reportAgent) StatusValue() agent.Status                     { return agent.StatusIdle }
func (*reportAgent) Cancel(agent.CancelCause, agent.CancelOptions) {}
func (*reportAgent) WhenIdle(context.Context) error                { return nil }
func (*reportAgent) RunMaintenance(context.Context, func(context.Context) error) error {
	return nil
}
func (*reportAgent) Send(agentmessage.UserMessage, agent.InboxTarget, bool) error { return nil }
func (*reportAgent) Followup(agentmessage.UserMessage) error                      { return nil }
func (subject *reportAgent) Steer(messageValue agentmessage.UserMessage) error {
	subject.mutex.Lock()
	subject.steered = append(subject.steered, messageValue)
	subject.mutex.Unlock()
	return nil
}
func (subject *reportAgent) Inject(messageValue agentmessage.UserMessage) error {
	subject.mutex.Lock()
	subject.injected = append(subject.injected, messageValue)
	subject.mutex.Unlock()
	return nil
}

var _ liveAgents = (*reportAgentRegistry)(nil)
var _ agent.Agent = (*reportAgent)(nil)
