package execution

import (
	"context"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
)

type publicationAgent struct {
	identifier session.SessionID
}

func (subject *publicationAgent) ID() session.SessionID {
	return subject.identifier
}

func (*publicationAgent) OptionsValue() agent.Options {
	return agent.Options{}
}

func (*publicationAgent) SessionValue() session.Context {
	return nil
}

func (*publicationAgent) InboxValue() *agent.Inbox {
	return nil
}

func (*publicationAgent) StatusValue() agent.Status {
	return agent.StatusIdle
}

func (*publicationAgent) Cancel(agent.CancelCause, agent.CancelOptions) {}

func (*publicationAgent) WhenIdle(context.Context) error {
	return nil
}

func (*publicationAgent) RunMaintenance(
	context.Context,
	func(context.Context) error,
) error {
	return nil
}

func (*publicationAgent) Followup(agentmessage.UserMessage) error {
	return nil
}

func (*publicationAgent) Steer(agentmessage.UserMessage) error {
	return nil
}

func (*publicationAgent) Inject(agentmessage.UserMessage) error {
	return nil
}

type publicationPublisher struct {
	parent agent.Agent
	fact   subagent.Started
	calls  int
}

func (publisher *publicationPublisher) PublishStarted(
	parentAgent agent.Agent,
	fact subagent.Started,
) {
	publisher.parent = parentAgent
	publisher.fact = fact
	publisher.calls++
}

func (*publicationPublisher) PublishEnded(agent.Agent, subagent.Ended) {}

func TestPublishActivatesRegistersAndAnnouncesExecution(t *testing.T) {
	parentAgent := &publicationAgent{
		identifier: session.SessionID("parent"),
	}
	childAgent := &publicationAgent{
		identifier: session.SessionID("child"),
	}
	running, err := New(
		subagent.RunID("run"),
		childAgent.ID(),
		&terminatorRecord{},
	)
	if err != nil {
		t.Fatal(err)
	}
	registryOwner := NewRegistry()
	lifecyclePublisher := &publicationPublisher{}
	closing := make(chan struct{})
	err = Publish(
		registryOwner,
		lifecyclePublisher,
		Entry{
			Execution: running,
			Mode:      subagent.ModeContinuable,
			Parent:    parentAgent,
			Subject:   childAgent,
			Closing:   closing,
		},
		"seed-builder",
	)
	if err != nil {
		t.Fatal(err)
	}
	if running.State() != subagent.ExecutionActive {
		t.Fatalf("state = %q", running.State())
	}
	publishedEntry, found := registryOwner.Find(childAgent.ID())
	if !found || publishedEntry.Execution != running {
		t.Fatalf("registry entry = %#v, found = %v", publishedEntry, found)
	}
	if lifecyclePublisher.calls != 1 ||
		!agent.Same(lifecyclePublisher.parent, parentAgent) ||
		lifecyclePublisher.fact.RunID != running.RunID() ||
		lifecyclePublisher.fact.ID != childAgent.ID() ||
		lifecyclePublisher.fact.Provider != "seed-builder" ||
		!lifecyclePublisher.fact.Local {
		t.Fatalf("started publication = %#v", lifecyclePublisher)
	}
}

func TestPublishDoesNotAnnounceRejectedRegistryEntry(t *testing.T) {
	parentAgent := &publicationAgent{
		identifier: session.SessionID("parent"),
	}
	childAgent := &publicationAgent{
		identifier: session.SessionID("child"),
	}
	otherAgent := &publicationAgent{
		identifier: session.SessionID("other"),
	}
	running, err := New(
		subagent.RunID("run"),
		childAgent.ID(),
		&terminatorRecord{},
	)
	if err != nil {
		t.Fatal(err)
	}
	lifecyclePublisher := &publicationPublisher{}
	err = Publish(
		NewRegistry(),
		lifecyclePublisher,
		Entry{
			Execution: running,
			Mode:      subagent.ModeBound,
			Parent:    parentAgent,
			Subject:   otherAgent,
			Closing:   make(chan struct{}),
		},
		"seed-builder",
	)
	if err == nil {
		t.Fatal("Publish accepted mismatched Execution and Agent identities")
	}
	if lifecyclePublisher.calls != 0 {
		t.Fatalf("started calls = %d", lifecyclePublisher.calls)
	}
}
