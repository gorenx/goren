package registry_test

import (
	"errors"
	"testing"

	"github.com/gorenx/goren/agent"
	agentactor "github.com/gorenx/goren/agent/actor"
	agentregistry "github.com/gorenx/goren/agent/registry"
)

func TestCompanionRolePersistsThroughOrdinaryRegistryCreation(t *testing.T) {
	system, authority, input := creationFixture(t)
	parent := askLifecycle[agentregistry.Ready](t, system, authority, input)
	if parent.Err != nil {
		t.Fatal(parent.Err)
	}
	child := agentregistry.CreateChildAgent{
		Parent: parent.Creation,
		Child: agentregistry.Creation{
			AgentID:    "reviewer",
			CreationID: "companion-first",
		},
		Role: agent.CompanionRole,
	}
	ready := askLifecycle[agentregistry.Ready](t, system, authority, child)
	if ready.Err != nil || ready.Registration.Role != agent.CompanionRole || ready.Registration.ParentID == nil || *ready.Registration.ParentID != parent.Creation.AgentID {
		t.Fatalf("Companion creation failed: %+v", ready)
	}
	changed := askLifecycle[agentregistry.Completion](t, system, authority, agentregistry.ReserveAgent{
		Creation: ready.Creation,
		Parent:   &parent.Creation,
	})
	if !errors.Is(changed.Err, agent.ErrRole) {
		t.Fatal("repeated reservation changed role")
	}
	nested := askLifecycle[agentregistry.Ready](t, system, authority, agentregistry.CreateChildAgent{
		Parent: ready.Creation,
		Child: agentregistry.Creation{
			AgentID:    "recursive",
			CreationID: "refused",
		},
		Role: agent.CompanionRole,
	})
	if !errors.Is(nested.Err, agent.ErrRole) {
		t.Fatalf("nested Companion accepted: %v", nested.Err)
	}
	if closed := askLifecycle[agentactor.Closed](t, system, authority, agentregistry.CloseAgent{
		Creation: ready.Creation,
	}); closed.Err != nil {
		t.Fatal(closed.Err)
	}
	child.Restore = true
	child.Child.CreationID = "downgrade"
	child.Role = agent.OrdinaryRole
	rejected := askLifecycle[agentregistry.Ready](t, system, authority, child)
	if !errors.Is(rejected.Err, agent.ErrRole) {
		t.Fatalf("restored Companion lost role: %v", rejected.Err)
	}
	if closed := askLifecycle[agentactor.Closed](t, system, authority, agentregistry.CloseAgent{
		Creation: child.Child,
	}); closed.Err != nil {
		t.Fatal(closed.Err)
	}
	child.Role = agent.CompanionRole
	child.Child.CreationID = "companion-restored"
	restored := askLifecycle[agentregistry.Ready](t, system, authority, child)
	if restored.Err != nil || restored.Registration.Role != agent.CompanionRole {
		t.Fatalf("authorized restoration failed: %v", restored.Err)
	}
	if closed := askLifecycle[agentactor.Closed](t, system, authority, agentregistry.CloseAgent{
		Creation: parent.Creation,
	}); closed.Err != nil {
		t.Fatal(closed.Err)
	}
}
