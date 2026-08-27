package bound

import (
	"context"
	"errors"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
)

func TestStartReturnsExplicitPendingError(t *testing.T) {
	t.Parallel()
	parentAgent := newBoundAgent(t, "parent")
	command, commandErr := subagent.NewBoundStart(parentAgent, "bound-child")
	if commandErr != nil {
		t.Fatal(commandErr)
	}
	running, startErr := New().Start(context.Background(), command)
	if running != nil || !errors.Is(startErr, ErrStartNotImplemented) {
		t.Fatalf("Start = (%v, %v)", running, startErr)
	}
}

func TestBoundStartCommandRejectsMissingBindingIdentity(t *testing.T) {
	t.Parallel()
	parentAgent := newBoundAgent(t, "parent")
	for _, testCase := range []struct {
		name    string
		parent  agent.Agent
		childID session.SessionID
	}{
		{
			name:    "nil parent",
			childID: "bound-child",
		},
		{
			name:   "empty child",
			parent: parentAgent,
		},
		{
			name:    "untrimmed child",
			parent:  parentAgent,
			childID: " bound-child",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if _, commandErr := subagent.NewBoundStart(
				testCase.parent,
				testCase.childID,
			); commandErr == nil {
				t.Fatal("invalid BoundStartCommand was accepted")
			}
		})
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

func (*boundAgent) Send(llm.UserMessage, agent.InboxTarget, bool) error {
	return nil
}

func (*boundAgent) Followup(llm.UserMessage) error {
	return nil
}

func (*boundAgent) Steer(llm.UserMessage) error {
	return nil
}

func (*boundAgent) Inject(llm.UserMessage) error {
	return nil
}

var _ agent.Agent = (*boundAgent)(nil)
