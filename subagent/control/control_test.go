package control

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

func TestControlToolsDelegateAuthorityToContinuableService(t *testing.T) {
	t.Parallel()
	caller := newControlAgent(t, "parent", agent.StatusRunning)
	continuations := &controlContinuation{}
	owner := &Plugin{
		continuations: continuations,
	}
	value, sendErr := owner.sendMessage(
		json.RawMessage(`{
  "subagent_id": "child",
  "message": "continue"
}`),
		tools.ToolRunContext{
			Context: context.Background(),
			Execution: tools.ToolExecution{
				Subject: caller,
			},
		},
	)
	if sendErr != nil {
		t.Fatal(sendErr)
	}
	if continuations.followParent != caller ||
		continuations.followChild != "child" ||
		string(value) != `{"messageId":"message"}` {
		t.Fatalf("send route = %#v value=%s", continuations, value)
	}
	_, interruptErr := owner.interrupt(
		json.RawMessage(`{"agent_id":"grandchild"}`),
		tools.ToolRunContext{
			Context: context.Background(),
			Execution: tools.ToolExecution{
				Subject: caller,
			},
		},
	)
	if interruptErr != nil {
		t.Fatal(interruptErr)
	}
	authority, matches := continuations.authority.(subagent.AncestorInterruptAuthority)
	if continuations.interrupted != "grandchild" ||
		!matches || authority.Agent != caller {
		t.Fatalf("interrupt route = %#v", continuations)
	}
}

func TestListProjectionOmitsOneShotAndUsesLiveRegistryStatus(t *testing.T) {
	t.Parallel()
	running := newControlAgent(t, "running", agent.StatusRunning)
	idle := newControlAgent(t, "idle", agent.StatusIdle)
	owner := &Plugin{
		agents: &controlRegistry{
			entries: map[session.SessionID]agent.Agent{
				"running": running,
				"idle":    idle,
			},
		},
	}
	for _, testCase := range []struct {
		id     session.SessionID
		status string
	}{
		{
			id:     "running",
			status: "running",
		},
		{
			id:     "idle",
			status: "idle",
		},
		{
			id:     "cold",
			status: "ready",
		},
	} {
		entry, included := owner.project(
			subagent.ContinuableChildEntry{
				ID:    testCase.id,
				Label: "worker",
			},
			nil,
			nil,
		)
		if !included || entry.Status != testCase.status {
			t.Fatalf("project %s = %#v, included=%v", testCase.id, entry, included)
		}
	}
	if _, included := owner.project(
		subagent.OneShotChildEntry{
			ID: "terminal",
		},
		nil,
		nil,
	); included {
		t.Fatal("one-shot child was exposed by list_agents")
	}
}

type controlContinuation struct {
	followParent agent.Agent
	followChild  session.SessionID
	interrupted  session.SessionID
	authority    subagent.InterruptAuthority
}

func (*controlContinuation) StartContinuable(
	context.Context,
	subagent.ContinuableStartSpec,
) (subagent.ContinuableStart, error) {
	return subagent.ContinuableStart{}, nil
}

func (record *controlContinuation) Followup(
	_ context.Context,
	parentAgent agent.Agent,
	childID session.SessionID,
	_ []llm.ContentBlock,
	_ subagent.FollowupOptions,
) (llm.MessageID, error) {
	record.followParent = parentAgent
	record.followChild = childID
	return "message", nil
}

func (record *controlContinuation) Interrupt(
	targetID session.SessionID,
	authority subagent.InterruptAuthority,
) error {
	record.interrupted = targetID
	record.authority = authority
	return nil
}

func (*controlContinuation) ReportFrom(
	context.Context,
	agent.Agent,
	[]llm.ContentBlock,
	subagent.ReportOptions,
) (llm.MessageID, error) {
	return "", nil
}

func (*controlContinuation) DrainContinuableChildren(
	context.Context,
	agent.Agent,
	[]session.SessionID,
) error {
	return nil
}

func (*controlContinuation) DrainContinuableDescendants(
	context.Context,
	[]agent.Agent,
) error {
	return nil
}

type controlRegistry struct {
	entries map[session.SessionID]agent.Agent
}

func (*controlRegistry) RegisterFactory(agent.Factory) (agent.FactoryRegistration, error) {
	return nil, nil
}
func (*controlRegistry) Create(context.Context, agent.CreateOptions) (agent.Handle, error) {
	return agent.Handle{}, nil
}
func (*controlRegistry) Resume(context.Context, agent.ResumeOptions) (agent.Handle, error) {
	return agent.Handle{}, nil
}
func (*controlRegistry) Enter(agent.Agent, agent.Agent) error        { return nil }
func (*controlRegistry) Announce(context.Context, agent.Agent) error { return nil }
func (*controlRegistry) Remove(context.Context, agent.Agent) error   { return nil }
func (registry *controlRegistry) Get(identifier session.SessionID) (agent.Agent, bool) {
	entry, found := registry.entries[identifier]
	return entry, found
}
func (registry *controlRegistry) Contains(subject agent.Agent) bool {
	entry, found := registry.Get(subject.ID())
	return found && entry == subject
}
func (*controlRegistry) IsOwnedBy(session.SessionID, agent.Agent) bool { return false }
func (registry *controlRegistry) List() []agent.Agent {
	result := make([]agent.Agent, 0, len(registry.entries))
	for _, entry := range registry.entries {
		result = append(result, entry)
	}
	return result
}
func (registry *controlRegistry) Roots() []agent.Agent { return registry.List() }

type controlAgent struct {
	plugin.Base
	id      session.SessionID
	status  agent.Status
	session *session.Session
}

func newControlAgent(
	t *testing.T,
	identifier session.SessionID,
	agentStatus agent.Status,
) *controlAgent {
	t.Helper()
	conversation, err := session.New(identifier, session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return &controlAgent{
		id:      identifier,
		status:  agentStatus,
		session: conversation,
	}
}

func (*controlAgent) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "test/control-agent",
	}
}
func (*controlAgent) Apply(context.Context) error                   { return nil }
func (*controlAgent) Dispose(context.Context) error                 { return nil }
func (subject *controlAgent) ID() session.SessionID                 { return subject.id }
func (*controlAgent) OptionsValue() agent.Options                   { return agent.Options{} }
func (subject *controlAgent) SessionValue() *session.Session        { return subject.session }
func (*controlAgent) InboxValue() *agent.Inbox                      { return nil }
func (subject *controlAgent) StatusValue() agent.Status             { return subject.status }
func (*controlAgent) Cancel(agent.CancelCause, agent.CancelOptions) {}
func (*controlAgent) WhenIdle(context.Context) error                { return nil }
func (*controlAgent) RunMaintenance(context.Context, func(context.Context) error) error {
	return nil
}
func (*controlAgent) Send(llm.UserMessage, agent.InboxTarget, bool) error { return nil }
func (*controlAgent) Followup(llm.UserMessage) error                      { return nil }
func (*controlAgent) Steer(llm.UserMessage) error                         { return nil }
func (*controlAgent) Inject(llm.UserMessage) error                        { return nil }

var _ subagent.ContinuableService = (*controlContinuation)(nil)
var _ agent.Registry = (*controlRegistry)(nil)
var _ agent.Agent = (*controlAgent)(nil)
