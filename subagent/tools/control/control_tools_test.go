package control

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/tools"
)

type childControlRecord struct {
	parent    session.SessionID
	child     session.SessionID
	target    session.SessionID
	authority subagent.InterruptAuthority
}

func (record *childControlRecord) Send(_ context.Context, parent session.SessionID, child session.SessionID, _ []agentmessage.ContentBlock, _ subagent.FollowupOptions) (agentmessage.MessageID, error) {
	record.parent = parent
	record.child = child
	return "message", nil
}

func (record *childControlRecord) Interrupt(_ context.Context, target session.SessionID, authority subagent.InterruptAuthority) error {
	record.target = target
	record.authority = authority
	return nil
}

type emptyDirectory struct{}

func (emptyDirectory) ListChildren(context.Context, session.SessionID) ([]subagent.ChildEntry, error) {
	return nil, nil
}

func (emptyDirectory) ListDescendants(context.Context, session.SessionID) ([]subagent.DescendantEntry, error) {
	return nil, nil
}

type activityRecord map[session.SessionID]agent.Status

func (record activityRecord) Status(_ context.Context, id session.SessionID) (agent.Status, bool, error) {
	status, found := record[id]
	return status, found, nil
}

func TestControlToolsUseInvocationSessionAsAuthority(t *testing.T) {
	children := &childControlRecord{}
	dispatch := &messageRecord{}
	adapter, err := newControlTools(dispatch, children, emptyDirectory{}, activityRecord{})
	if err != nil {
		t.Fatal(err)
	}
	run := tools.ToolRunContext{
		Context: context.Background(),
		Execution: tools.Invocation{
			SessionID: "parent",
		},
	}
	value, err := adapter.sendMessageDefinition().Executor.Execute(json.RawMessage(`{"subagent_id":"child","message":"continue"}`), run)
	if err != nil || dispatch.parent != "parent" || dispatch.child != "child" || string(value) != `{"messageId":"message"}` {
		t.Fatalf("send value = %s, record = %#v, error = %v", value, children, err)
	}
	_, err = adapter.interruptDefinition().Executor.Execute(json.RawMessage(`{"agent_id":"grandchild"}`), run)
	authority, valid := children.authority.(subagent.AncestorInterruptAuthority)
	if err != nil || children.target != "grandchild" || !valid || authority.AgentID != "parent" {
		t.Fatalf("interrupt record = %#v, error = %v", children, err)
	}
}

func TestListProjectionUsesActivityReaderAndOmitsOneShot(t *testing.T) {
	adapter := &listTool{
		directory: emptyDirectory{},
		activity: activityRecord{
			"running": agent.StatusRunning,
		},
	}
	entry, included, err := adapter.project(context.Background(), subagent.ContinuableChildEntry{
		ID:    "running",
		Label: "worker",
	}, nil, nil)
	if err != nil || !included || entry.Status != "running" {
		t.Fatalf("entry = %#v, included = %v, error = %v", entry, included, err)
	}
	_, included, err = adapter.project(context.Background(), subagent.OneShotChildEntry{
		ID: "terminal",
	}, nil, nil)
	if err != nil || included {
		t.Fatalf("included = %v, error = %v", included, err)
	}
}

// messageRecord exercises the message capability independently of interruption.
type messageRecord struct {
	parent session.SessionID
	child  session.SessionID
}

func (record *messageRecord) Send(_ context.Context, input Message) (agentmessage.MessageID, error) {
	record.parent = input.ParentSessionID
	record.child = input.ChildSessionID
	return "message", nil
}
