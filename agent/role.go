package agent

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/gorenx/goren/session"
)

// Role is the immutable Agent identity used for creation admission.
// The zero value is an ordinary Agent, including ordinary delegated children.
type Role string

const (
	OrdinaryRole  Role = ""
	CompanionRole Role = "companion"
)

// ErrRole rejects an invalid role or a role inconsistent with persisted identity.
var ErrRole = errors.New("agent: invalid or conflicting role")

// RoleRecord identifies the Session whose role was fixed by trusted creation.
// A copied ancestor record cannot establish a new Session's role.
type RoleRecord struct {
	SessionID session.SessionID `json:"sessionID"`
	Role      Role              `json:"role"`
}

// RoleRecorded persists Companion identity across ordinary Session recovery.
var RoleRecorded = session.DefineEvent[RoleRecord]("agent/role")

// Valid reports whether the role belongs to this build's identity contract.
func (selection Role) Valid() bool { return selection == OrdinaryRole || selection == CompanionRole }

// RoleFromEvents reads the current Session's identity, excluding inherited seed.
// Repeated or ordinary-role records are invalid: only Companion requires a fact.
func RoleFromEvents(identifier session.SessionID, entries []session.Event) (Role, error) {
	if identifier == "" {
		return OrdinaryRole, ErrRole
	}
	selected := OrdinaryRole
	for _, entry := range entries {
		if entry.Type != "agent/role" {
			continue
		}
		var recorded RoleRecord
		if json.Unmarshal(entry.Data, &recorded) != nil || recorded.SessionID == "" || recorded.Role != CompanionRole {
			return OrdinaryRole, ErrRole
		}
		if recorded.SessionID != identifier {
			continue
		}
		if selected != OrdinaryRole {
			return OrdinaryRole, ErrRole
		}
		selected = recorded.Role
	}
	return selected, nil
}

// RoleAdmission verifies restored identity or records a new Companion identity.
// The Session owner runs it atomically with all other writes. Registry flushes
// the returned barrier before proceeding to dependent runtime components.
type RoleAdmission struct {
	SessionID session.SessionID
	Expected  Role
	Restore   bool
}

// Build does not promote existing ordinary history or downgrade a Companion.
func (input RoleAdmission) Build(_ context.Context, snapshot session.Snapshot) ([]session.EventDraft, error) {
	if !input.Expected.Valid() {
		return nil, ErrRole
	}
	recorded, err := RoleFromEvents(input.SessionID, snapshot.Events)
	if err != nil {
		return nil, err
	}
	if recorded == input.Expected {
		return nil, nil
	}
	if input.Restore || recorded != OrdinaryRole || input.Expected != CompanionRole {
		return nil, ErrRole
	}
	draft, err := session.NewEventDraft(RoleRecorded, RoleRecord{
		SessionID: input.SessionID,
		Role:      CompanionRole,
	})
	if err != nil {
		return nil, err
	}
	return []session.EventDraft{draft}, nil
}
