package bound

import (
	"context"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
)

type extensionsStub struct {
	setup func([]string) (agent.Setup, error)
}

func (stub extensionsStub) Setup(
	names []string,
) (agent.Setup, error) {
	if stub.setup == nil {
		return nil, nil
	}
	return stub.setup(names)
}

func TestNewRejectsIncompleteDependencies(t *testing.T) {
	t.Parallel()
	if owner, err := New(context.Background(), Dependencies{}); err == nil || owner != nil {
		t.Fatalf("New = (%v, %v), want nil, error", owner, err)
	}
}

type boundAgent struct {
	id      session.SessionID
	session session.Context
}

func newBoundAgent(t *testing.T, identifier session.SessionID) *boundAgent {
	t.Helper()
	conversation, err := session.New(identifier, session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return &boundAgent{
		id:      identifier,
		session: conversation,
	}
}

func (subject *boundAgent) ID() session.SessionID {
	return subject.id
}

func (*boundAgent) OptionsValue() agent.Options {
	return agent.Options{}
}

func (subject *boundAgent) SessionValue() session.Context {
	return subject.session
}

func (*boundAgent) InboxValue() *agent.Inbox {
	return nil
}

func (*boundAgent) StatusValue() agent.Status {
	return agent.StatusIdle
}

func (*boundAgent) Cancel(agent.CancelCause, agent.CancelOptions) {}

func (*boundAgent) WhenIdle(context.Context) error {
	return nil
}

func (*boundAgent) RunMaintenance(
	context.Context,
	func(context.Context) error,
) error {
	return nil
}

func (*boundAgent) Send(agentmessage.UserMessage, agent.InboxTarget, bool) error {
	return nil
}

func (*boundAgent) Followup(agentmessage.UserMessage) error {
	return nil
}

func (*boundAgent) Steer(agentmessage.UserMessage) error {
	return nil
}

func (*boundAgent) Inject(agentmessage.UserMessage) error {
	return nil
}

var _ agent.Agent = (*boundAgent)(nil)
var _ Extensions = extensionsStub{}
