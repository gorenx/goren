package fork

import (
	"context"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
)

func TestCompletedTurnPrefixExcludesInflightTurn(t *testing.T) {
	t.Parallel()
	conversation, err := session.New("parent", session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	appendTurnBoundary(t, conversation, 1, true)
	completedLength := len(conversation.Events())
	appendTurnBoundary(t, conversation, 2, false)
	prefix := completedTurnPrefix(&forkAgent{
		id:      "parent",
		session: conversation,
	})
	if len(prefix) != completedLength ||
		prefix[len(prefix)-1].Type != session.TurnEndEventName {
		t.Fatalf("prefix = %#v", prefix)
	}
}

func appendTurnBoundary(
	t *testing.T,
	conversation *session.Session,
	turn int64,
	completed bool,
) {
	t.Helper()
	if _, err := session.AppendSerialized(
		conversation,
		session.TurnStarted,
		session.TurnStart{
			Turn: turn,
		},
	); err != nil {
		t.Fatal(err)
	}
	if !completed {
		return
	}
	if _, err := session.AppendSerialized(
		conversation,
		session.TurnEnded,
		session.TurnEnd{
			Turn:   turn,
			Reason: session.TurnCompleted{},
		},
	); err != nil {
		t.Fatal(err)
	}
}

type forkAgent struct {
	plugin.Base
	id      session.SessionID
	session *session.Session
}

func (*forkAgent) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "test/fork-agent",
	}
}
func (*forkAgent) Apply(context.Context) error                   { return nil }
func (*forkAgent) Dispose(context.Context) error                 { return nil }
func (subject *forkAgent) ID() session.SessionID                 { return subject.id }
func (*forkAgent) OptionsValue() agent.Options                   { return agent.Options{} }
func (subject *forkAgent) SessionValue() *session.Session        { return subject.session }
func (*forkAgent) InboxValue() *agent.Inbox                      { return nil }
func (*forkAgent) StatusValue() agent.Status                     { return agent.StatusIdle }
func (*forkAgent) Cancel(agent.CancelCause, agent.CancelOptions) {}
func (*forkAgent) WhenIdle(context.Context) error                { return nil }
func (*forkAgent) RunMaintenance(context.Context, func(context.Context) error) error {
	return nil
}
func (*forkAgent) Send(llm.UserMessage, agent.InboxTarget, bool) error { return nil }
func (*forkAgent) Followup(llm.UserMessage) error                      { return nil }
func (*forkAgent) Steer(llm.UserMessage) error                         { return nil }
func (*forkAgent) Inject(llm.UserMessage) error                        { return nil }

var _ agent.Agent = (*forkAgent)(nil)
