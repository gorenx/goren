package execution

import (
	"testing"

	agentregistry "github.com/gorenx/goren/agent/registry"
	"github.com/gorenx/goren/subagent"
)

type publicationPublisher struct {
	parent agentregistry.Creation
	fact   subagent.Started
	calls  int
}

func (publisher *publicationPublisher) PublishStarted(parent agentregistry.Creation, fact subagent.Started) {
	publisher.parent = parent
	publisher.fact = fact
	publisher.calls++
}

func (*publicationPublisher) PublishEnded(agentregistry.Creation, subagent.Ended) {}

func TestPublishActivatesRegistersAndAnnouncesExecution(t *testing.T) {
	parent := agentregistry.Creation{
		AgentID:    "parent",
		CreationID: "parent-run",
	}
	child := agentregistry.Creation{
		AgentID:    "child",
		CreationID: "child-run",
	}
	running := newManagedExecutionRecord("run", child.AgentID)
	registryOwner := NewRegistry()
	publisher := &publicationPublisher{}
	err := Publish(
		registryOwner,
		publisher,
		Entry{
			Execution: running,
			Mode:      subagent.ModeContinuable,
			Parent:    parent,
			Subject:   child,
		},
		"seed-builder",
	)
	if err != nil {
		t.Fatal(err)
	}
	published, found := registryOwner.Find(child.AgentID)
	if running.State() != subagent.ExecutionActive || !found || published.Execution != running {
		t.Fatalf("published = %#v, found = %v", published, found)
	}
	if publisher.calls != 1 || publisher.parent != parent ||
		publisher.fact.RunID != running.RunID() || publisher.fact.ID != child.AgentID ||
		publisher.fact.Provider != "seed-builder" || !publisher.fact.Local {
		t.Fatalf("publication = %#v", publisher)
	}
}

func TestPublishRejectsMismatchedExecutionIdentityBeforeAnnouncement(t *testing.T) {
	running := newManagedExecutionRecord("run", "child")
	publisher := &publicationPublisher{}
	err := Publish(
		NewRegistry(),
		publisher,
		Entry{
			Execution: running,
			Mode:      subagent.ModeOneShot,
			Parent: agentregistry.Creation{
				AgentID:    "parent",
				CreationID: "parent-run",
			},
			Subject: agentregistry.Creation{
				AgentID:    "other",
				CreationID: "other-run",
			},
		},
		"seed-builder",
	)
	if err == nil || publisher.calls != 0 {
		t.Fatalf("error = %v, calls = %d", err, publisher.calls)
	}
}
