package agent_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
)

func TestRoleAdmissionUsesOwnHistoryAndRefusesRoleReplacement(t *testing.T) {
	inherited := []session.Event{
		{
			Type: "agent/role",
			Data: json.RawMessage(`{"sessionID":"ancestor","role":"companion"}`),
		},
		{
			Type: session.EndSeedEventName,
		},
	}
	selected, err := agent.RoleFromEvents("current", inherited)
	if err != nil || selected != agent.OrdinaryRole {
		t.Fatal("inherited history controls current role")
	}
	own := append(inherited, session.Event{
		Type: "agent/role",
		Data: json.RawMessage(`{"sessionID":"current","role":"companion"}`),
	})
	for _, entry := range []struct {
		name    string
		plan    agent.RoleAdmission
		events  []session.Event
		invalid bool
		drafts  int
	}{
		{
			name: "new companion",
			plan: agent.RoleAdmission{
				SessionID: "current",
				Expected:  agent.CompanionRole,
			},
			events: inherited,
			drafts: 1,
		},
		{
			name: "restore companion",
			plan: agent.RoleAdmission{
				SessionID: "current",
				Expected:  agent.CompanionRole,
				Restore:   true,
			},
			events: own,
		},
		{
			name: "reject downgrade",
			plan: agent.RoleAdmission{
				SessionID: "current",
				Restore:   true,
			},
			events:  own,
			invalid: true,
		},
		{
			name: "reject promotion",
			plan: agent.RoleAdmission{
				SessionID: "current",
				Expected:  agent.CompanionRole,
				Restore:   true,
			},
			events:  inherited,
			invalid: true,
		},
	} {
		t.Run(entry.name, func(t *testing.T) {
			drafts, err := entry.plan.Build(t.Context(), session.Snapshot{
				Events: entry.events,
			})
			if errors.Is(err, agent.ErrRole) != entry.invalid || len(drafts) != entry.drafts {
				t.Fatalf("drafts=%d error=%v", len(drafts), err)
			}
		})
	}
}

func TestRegistryRejectsCompanionParentBeforeRegistration(t *testing.T) {
	var directory agent.Registry
	root := session.SessionID("parent")
	child := session.SessionID("companion")
	if err := directory.Register(agent.Registration{
		ID: root,
	}); err != nil {
		t.Fatal(err)
	}
	if err := directory.Register(agent.Registration{
		ID:       child,
		ParentID: &root,
		Role:     agent.CompanionRole,
	}); err != nil {
		t.Fatal(err)
	}
	for _, entry := range []agent.Registration{
		{
			ID:   "orphan",
			Role: agent.CompanionRole,
		},
		{
			ID:       "recursive",
			ParentID: &child,
			Role:     agent.CompanionRole,
		},
	} {
		if err := directory.Register(entry); !errors.Is(err, agent.ErrRole) {
			t.Fatalf("invalid parent accepted: %v", err)
		}
		if _, found := directory.Get(entry.ID); found {
			t.Fatal("rejected child retained registration")
		}
	}
	ordinary := session.SessionID("ordinary-child")
	if err := directory.Register(agent.Registration{
		ID:       ordinary,
		ParentID: &root,
	}); err != nil {
		t.Fatal(err)
	}
	if err := directory.Register(agent.Registration{
		ID:       "ordinary-child-companion",
		ParentID: &ordinary,
		Role:     agent.CompanionRole,
	}); err != nil {
		t.Fatal(err)
	}
}
