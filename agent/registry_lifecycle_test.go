package agent_test

import (
	"errors"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
)

func TestRegistryReadinessUsesDependenciesWithoutAStartupCursor(t *testing.T) {
	directory := &agent.Registry{}
	identifier := session.SessionID("conversation")
	err := directory.Register(agent.Registration{ID: identifier})
	if err != nil {
		t.Fatal(err)
	}
	err = directory.BeginInitialization(identifier, agent.Requirements{
		Commands:   true,
		Components: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	prompt := agent.Component{Kind: agent.PromptComponent}
	err = directory.ConfirmComponent(identifier, prompt)
	if err == nil {
		t.Fatal("prompt became ready before Session")
	}
	err = directory.CompleteInitialization(identifier)
	if err == nil {
		t.Fatal("published incomplete Agent")
	}
	conversation := agent.Component{Kind: agent.SessionComponent}
	err = directory.ConfirmComponent(identifier, conversation)
	if err != nil {
		t.Fatal(err)
	}
	err = directory.ConfirmComponent(identifier, prompt)
	if err == nil {
		t.Fatal("prompt became ready before tool definitions")
	}
	// Model restoration depends on Session, independently of Prompt readiness.
	err = directory.ConfirmComponent(identifier, agent.Component{Kind: agent.ModelComponent})
	if err != nil {
		t.Fatal(err)
	}
	executor := agent.Component{Kind: agent.AgentComponent}
	err = directory.ConfirmComponent(identifier, executor)
	if err == nil {
		t.Fatal("execution ignored missing Prompt")
	}
	for _, dependency := range []agent.Component{
		{Kind: agent.ToolsComponent},
		prompt,
		executor,
		{Kind: agent.CommandsComponent},
		{
			Kind:  agent.AdditionalComponent,
			Index: 0,
		},
		{
			Kind:  agent.AdditionalComponent,
			Index: 1,
		},
	} {
		err := directory.ConfirmComponent(identifier, dependency)
		if err != nil {
			t.Fatal(err)
		}
	}
	err = directory.CompleteInitialization(identifier)
	if err != nil {
		t.Fatal(err)
	}
	err = directory.CheckReady(identifier)
	if err != nil {
		t.Fatal(err)
	}
	err = directory.BeginClose(identifier)
	if err != nil {
		t.Fatal(err)
	}
	err = directory.CompleteInitialization(identifier)
	if !errors.Is(err, agent.ErrClosing) {
		t.Fatal("closing Agent became available", err)
	}
}

func TestRegistryFailureRetainsUnconfirmedResourcesAndOriginalError(t *testing.T) {
	directory := &agent.Registry{}
	identifier := session.SessionID("conversation")
	err := directory.Register(agent.Registration{ID: identifier})
	if err != nil {
		t.Fatal(err)
	}
	err = directory.BeginInitialization(identifier, agent.Requirements{})
	if err != nil {
		t.Fatal(err)
	}
	err = directory.AcquireComponent(identifier, agent.Component{Kind: agent.SessionComponent})
	if err != nil {
		t.Fatal(err)
	}
	original := errors.New("registration reply lost")
	err = directory.FailInitialization(identifier, original)
	if err != nil {
		t.Fatal(err)
	}
	state, _ := directory.State(identifier)
	if state != agent.Closing {
		t.Fatal(state)
	}
	for range 2 {
		resource, pending, err := directory.NextRelease(identifier)
		if err != nil || !pending || resource.Kind != agent.Conversation {
			t.Fatalf("responsibility = %+v, %v", resource, err)
		}
		err = directory.Remove(identifier)
		if err == nil {
			t.Fatal("unconfirmed resource was forgotten")
		}
	}
	err = directory.FailInitialization(identifier, errors.New("cleanup also failed"))
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(directory.InitializationError(identifier), original) {
		t.Fatal("original error overwritten")
	}
	err = directory.Register(agent.Registration{ID: identifier})
	if err == nil {
		t.Fatal("replacement stole resource responsibility")
	}
	err = directory.ConfirmRelease(identifier, agent.Resource{Kind: agent.Conversation})
	if err != nil {
		t.Fatal(err)
	}
	err = directory.Remove(identifier)
	if err != nil {
		t.Fatal(err)
	}
	err = directory.Remove(identifier)
	if err != nil {
		t.Fatal("close not idempotent", err)
	}
	if !directory.IsClosed(identifier) {
		t.Fatal("close not retained")
	}
	err = directory.Register(agent.Registration{ID: identifier})
	if err != nil {
		t.Fatal(err)
	}
	if directory.InitializationError(identifier) != nil {
		t.Fatal("replacement inherited failure")
	}
}

func TestRegistryChoosesOnlyRemainingResourceResponsibilities(t *testing.T) {
	directory := &agent.Registry{}
	parent := session.SessionID("parent")
	child := session.SessionID("child")
	err := directory.Register(agent.Registration{ID: parent})
	if err != nil {
		t.Fatal(err)
	}
	err = directory.Register(agent.Registration{
		ID:       child,
		ParentID: &parent,
	})
	if err != nil {
		t.Fatal(err)
	}
	expected := []agent.Resource{
		{Kind: agent.Work},
		{
			Kind: agent.Child,
			Name: string(child),
		},
		{Kind: agent.CommandRuntime},
		{
			Kind:  agent.Capability,
			Index: 1,
		},
		{
			Kind:  agent.Capability,
			Index: 0,
		},
		{Kind: agent.Execution},
		{Kind: agent.ToolsRuntime},
		{Kind: agent.PromptRuntime},
		{Kind: agent.Conversation},
		{Kind: agent.Activation},
	}
	for index := len(expected) - 1; index >= 0; index-- {
		owned := expected[index]
		if owned.Kind == agent.Child {
			continue
		}
		err := directory.TrackResource(parent, owned)
		if err != nil {
			t.Fatal(err)
		}
	}
	err = directory.BeginClose(parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range expected {
		got, pending, err := directory.NextRelease(parent)
		if err != nil || !pending || got != want {
			t.Fatalf("release = %+v, want %+v: %v", got, want, err)
		}
		if got.Kind == agent.Child {
			err := directory.BeginClose(child)
			if err != nil {
				t.Fatal(err)
			}
			err = directory.Remove(child)
			if err != nil {
				t.Fatal(err)
			}
		} else {
			err := directory.ConfirmRelease(parent, got)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	_, pending, err := directory.NextRelease(parent)
	if err != nil || pending {
		t.Fatal("completed responsibilities remain", err)
	}
	err = directory.Remove(parent)
	if err != nil {
		t.Fatal(err)
	}
}
