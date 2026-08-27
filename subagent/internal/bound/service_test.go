package bound

import (
	"context"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
)

type boundExtensionsRecord struct {
	validate  func([]string) error
	provision func([]string) (agent.Provisioner, error)
}

func (record boundExtensionsRecord) Validate(names []string) error {
	if record.validate == nil {
		return nil
	}
	return record.validate(names)
}

func (record boundExtensionsRecord) Provision(
	names []string,
) (agent.Provisioner, error) {
	if record.provision == nil {
		return nil, nil
	}
	return record.provision(names)
}

func TestNewRequiresExtensionSelection(t *testing.T) {
	t.Parallel()
	if owner, err := New(Dependencies{}); err == nil || owner != nil {
		t.Fatalf("New = (%v, %v), want nil, error", owner, err)
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
var _ Extensions = boundExtensionsRecord{}
