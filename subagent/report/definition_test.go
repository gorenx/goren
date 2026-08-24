package report

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/tools"
)

func TestReportUsesExactChildAndConfiguredDelivery(t *testing.T) {
	t.Parallel()
	childAgent := newReportAgent(t, "child")
	continuations := &reportContinuation{}
	contribution := &childPlugin{
		continuations: continuations,
		delivery:      subagent.ReportQuiet,
	}
	value, executeErr := contribution.execute(
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
	if continuations.child != childAgent ||
		continuations.delivery != subagent.ReportQuiet ||
		continuations.content != "review complete" ||
		string(value) != `{"messageId":"report-message"}` {
		t.Fatalf("report route = %#v value=%s", continuations, value)
	}
}

type reportContinuation struct {
	child    agent.Agent
	delivery subagent.ReportDelivery
	content  string
}

func (*reportContinuation) StartContinuable(
	context.Context,
	subagent.ContinuableStartSpec,
) (subagent.ContinuableStart, error) {
	return subagent.ContinuableStart{}, nil
}

func (*reportContinuation) Followup(
	context.Context,
	agent.Agent,
	session.SessionID,
	[]llm.ContentBlock,
	subagent.FollowupOptions,
) (llm.MessageID, error) {
	return "", nil
}

func (*reportContinuation) Interrupt(
	session.SessionID,
	subagent.InterruptAuthority,
) error {
	return nil
}

func (record *reportContinuation) ReportFrom(
	_ context.Context,
	childAgent agent.Agent,
	content []llm.ContentBlock,
	options subagent.ReportOptions,
) (llm.MessageID, error) {
	record.child = childAgent
	record.delivery = options.Delivery
	if len(content) == 1 {
		plainText, matches := content[0].(llm.PlainTextContent)
		if matches {
			record.content, _ = plainText.PlainText()
		}
	}
	return "report-message", nil
}

func (*reportContinuation) DrainContinuableChildren(
	context.Context,
	agent.Agent,
	[]session.SessionID,
) error {
	return nil
}

func (*reportContinuation) DrainContinuableDescendants(
	context.Context,
	[]agent.Agent,
) error {
	return nil
}

type reportAgent struct {
	plugin.Base
	id      session.SessionID
	session *session.Session
}

func newReportAgent(t *testing.T, identifier session.SessionID) *reportAgent {
	t.Helper()
	conversation, err := session.New(identifier, session.CreateOptions{})
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
func (subject *reportAgent) SessionValue() *session.Session        { return subject.session }
func (*reportAgent) InboxValue() *agent.Inbox                      { return nil }
func (*reportAgent) StatusValue() agent.Status                     { return agent.StatusIdle }
func (*reportAgent) Cancel(agent.CancelCause, agent.CancelOptions) {}
func (*reportAgent) WhenIdle(context.Context) error                { return nil }
func (*reportAgent) RunMaintenance(context.Context, func(context.Context) error) error {
	return nil
}
func (*reportAgent) Send(llm.UserMessage, agent.InboxTarget, bool) error { return nil }
func (*reportAgent) Followup(llm.UserMessage) error                      { return nil }
func (*reportAgent) Steer(llm.UserMessage) error                         { return nil }
func (*reportAgent) Inject(llm.UserMessage) error                        { return nil }

var _ subagent.ContinuableService = (*reportContinuation)(nil)
var _ agent.Agent = (*reportAgent)(nil)
