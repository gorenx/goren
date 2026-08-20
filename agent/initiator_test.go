package agent_test

import (
	"context"
	"testing"

	agentcore "github.com/gorenx/goren/agent"
)

func TestInitiatorContextIsExplicitNestedAndClearable(t *testing.T) {
	t.Parallel()
	parent := newFakeAgent(t, "initiator-parent")
	child := newFakeAgent(t, "initiator-child")

	parentContext, err := agentcore.WithInitiator(context.Background(), parent)
	if err != nil {
		t.Fatal(err)
	}
	if resolved, found := agentcore.InitiatorFrom(parentContext); !found || resolved != parent {
		t.Fatal("parent initiator was not preserved")
	}
	childContext, err := agentcore.WithInitiator(parentContext, child)
	if err != nil {
		t.Fatal(err)
	}
	if resolved, err := agentcore.RequireInitiator(childContext); err != nil || resolved != child {
		t.Fatalf("child initiator = %#v, %v", resolved, err)
	}
	clearedContext, err := agentcore.WithoutInitiator(childContext)
	if err != nil {
		t.Fatal(err)
	}
	if _, found := agentcore.InitiatorFrom(clearedContext); found {
		t.Fatal("clearing boundary retained initiator")
	}
	if resolved, found := agentcore.InitiatorFrom(parentContext); !found || resolved != parent {
		t.Fatal("nested contexts mutated parent attribution")
	}
}
