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
	children := &controlChildren{}
	adapter, err := newControlTools(
		children,
		emptyDirectory{},
		&controlRegistry{
			entries: map[session.SessionID]agent.Agent{},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	value, sendErr := adapter.sendMessage(
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
	if children.followParent != caller ||
		children.followChild != "child" ||
		string(value) != `{"messageId":"message"}` {
		t.Fatalf("send route = %#v value=%s", children, value)
	}
	_, interruptErr := adapter.interrupt(
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
	authority, matches := children.authority.(subagent.AncestorInterruptAuthority)
	if children.interrupted != "grandchild" ||
		!matches || authority.Agent != caller {
		t.Fatalf("interrupt route = %#v", children)
	}
}

func TestListProjectionOmitsOneShotAndUsesLiveRegistryStatus(t *testing.T) {
	t.Parallel()
	running := newControlAgent(t, "running", agent.StatusRunning)
	idle := newControlAgent(t, "idle", agent.StatusIdle)
	adapter, err := newControlTools(
		&controlChildren{},
		emptyDirectory{},
		&controlRegistry{
			entries: map[session.SessionID]agent.Agent{
				"running": running,
				"idle":    idle,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, testCase := range []struct {
		id     session.SessionID
		status string
		bound  bool
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
		{
			id:     "bound",
			status: "ready",
			bound:  true,
		},
	} {
		var childEntry subagent.ChildEntry = subagent.ContinuableChildEntry{
			ID:    testCase.id,
			Label: "worker",
		}
		if testCase.bound {
			childEntry = subagent.BoundChildEntry{
				ID:    testCase.id,
				Label: "worker",
			}
		}
		entry, included := adapter.project(
			childEntry,
			nil,
			nil,
		)
		if !included || entry.Status != testCase.status {
			t.Fatalf("project %s = %#v, included=%v", testCase.id, entry, included)
		}
	}
	if _, included := adapter.project(
		subagent.OneShotChildEntry{
			ID: "terminal",
		},
		nil,
		nil,
	); included {
		t.Fatal("one-shot child was exposed by list_agents")
	}
}

type controlChildren struct {
	followParent agent.Agent
	followChild  session.SessionID
	interrupted  session.SessionID
	authority    subagent.InterruptAuthority
}

func (record *controlChildren) Send(
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

func (record *controlChildren) Interrupt(
	_ context.Context,
	targetID session.SessionID,
	authority subagent.InterruptAuthority,
) error {
	record.interrupted = targetID
	record.authority = authority
	return nil
}

type controlRegistry struct {
	entries map[session.SessionID]agent.Agent
}

type emptyDirectory struct{}

func (emptyDirectory) ListChildren(
	context.Context,
	session.SessionID,
) ([]subagent.ChildEntry, error) {
	return nil, nil
}

func (emptyDirectory) ListDescendants(
	context.Context,
	session.SessionID,
) ([]subagent.DescendantEntry, error) {
	return nil, nil
}

func (registry *controlRegistry) Get(identifier session.SessionID) (agent.Agent, bool) {
	entry, found := registry.entries[identifier]
	return entry, found
}
func (registry *controlRegistry) Contains(subject agent.Agent) bool {
	entry, found := registry.Get(subject.ID())
	return found && entry == subject
}
func (registry *controlRegistry) List() []agent.Agent {
	result := make([]agent.Agent, 0, len(registry.entries))
	for _, entry := range registry.entries {
		result = append(result, entry)
	}
	return result
}

type controlAgent struct {
	plugin.Base
	id      session.SessionID
	status  agent.Status
	session session.Context
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
func (subject *controlAgent) SessionValue() session.Context         { return subject.session }
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

var _ subagent.ChildControl = (*controlChildren)(nil)
var _ subagent.ChildDirectory = emptyDirectory{}
var _ agent.Registry = (*controlRegistry)(nil)
var _ agent.Agent = (*controlAgent)(nil)
