package sessionapi

import (
	"context"
	"errors"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/session/persistence"
)

type activationAgent struct {
	Agent
	header session.Header
	parent *session.SessionID
	role   agent.Role
}

func (subject activationAgent) ID() session.SessionID        { return subject.header.ID }
func (subject activationAgent) ParentID() *session.SessionID { return subject.parent }
func (subject activationAgent) Role() agent.Role             { return subject.role }

type activationDirectory struct {
	current  Agent
	created  Agent
	opening  Opening
	resolves int
	failure  error
}

func (directory *activationDirectory) Resolve(context.Context, session.SessionID) (Agent, error) {
	directory.resolves++
	return directory.current, nil
}
func (directory *activationDirectory) Create(_ context.Context, input Opening) (Agent, error) {
	directory.opening = input
	directory.current = directory.created
	return directory.created, directory.failure
}

type activationHistory struct {
	History
	header  session.Header
	events  []session.Event
	failure error
}

func (source activationHistory) Inspect(context.Context, session.SessionID) (persistence.Inspection, error) {
	return persistence.Inspection{
		Header: source.header,
		Events: source.events,
	}, source.failure
}

func TestActivationUsesCurrentParentRatherThanHistoricalOrigin(t *testing.T) {
	cwd := "/project"
	header := session.Header{
		ID:     "child",
		CWD:    &cwd,
		Origin: session.OriginSubagent,
	}
	directory := &activationDirectory{
		created: activationAgent{
			header: header,
		},
	}
	access := &activation{
		agents: directory,
		history: activationHistory{
			header: header,
		},
	}
	subject, err := access.ordinary(context.Background(), header.ID)
	if err != nil || subject == nil || !directory.opening.Restore {
		t.Fatalf("root recovery: %v %+v", err, directory.opening)
	}
	parent := session.SessionID("parent")
	directory.current = activationAgent{
		header: header,
		parent: &parent,
	}
	_, err = access.ordinary(context.Background(), header.ID)
	var ownership *SubagentOwnershipError
	if !errors.As(err, &ownership) {
		t.Fatalf("active child accepted: %v", err)
	}
	directory.current = activationAgent{
		header: header,
		parent: &parent,
		role:   agent.CompanionRole,
	}
	if _, err := access.ordinary(context.Background(), header.ID); err != nil {
		t.Fatalf("live Companion was refused: %v", err)
	}
}

func TestActivationDoesNotRestoreCompanionHistoryAsRoot(t *testing.T) {
	header := session.Header{
		ID:     "companion",
		Origin: session.OriginSubagent,
	}
	directory := &activationDirectory{}
	access := &activation{
		agents: directory,
		history: activationHistory{
			header: header,
			events: []session.Event{{
				Type: "agent/role",
				Data: []byte(`{"sessionID":"companion","role":"companion"}`),
			}},
		},
	}
	_, err := access.ordinary(context.Background(), header.ID)
	var ownership *SubagentOwnershipError
	if !errors.As(err, &ownership) || directory.resolves != 1 || directory.opening.ID != "" {
		t.Fatalf("Companion history was opened as root: err=%v opening=%+v", err, directory.opening)
	}
}

func TestActivationChecksTheWinningCreationAgainstEachRequest(t *testing.T) {
	cwd := "/first"
	directory := &activationDirectory{
		created: activationAgent{
			header: session.Header{
				ID:  "shared",
				CWD: &cwd,
			},
		},
		failure: errors.New("another creation won"),
	}
	ensured := false
	access := &activation{
		agents: directory,
		sessions: activationSessions{
			header: session.Header{
				ID:  "shared",
				CWD: &cwd,
			},
		},
		history: activationHistory{
			failure: &persistence.NotFoundError{
				ID: "shared",
			},
		},
		directories: DirectoryProvisionerFunc(func(string) error { ensured = true; return nil }),
	}
	_, err := access.ensure(context.Background(), "shared", "/second", nil)
	var conflict *CWDConflictError
	if !errors.As(err, &conflict) || !ensured || directory.resolves != 2 {
		t.Fatalf("race adoption: %v resolves=%d", err, directory.resolves)
	}
	if conflict.RequestedCWD() != "/second" || *conflict.ExistingCWD() != cwd {
		t.Fatal("request-specific conflict lost")
	}
}

type activationSessions struct {
	LiveSessions
	header session.Header
}

func (source activationSessions) Read(context.Context, session.SessionID) (*Snapshot, error) {
	return &Snapshot{
		Header: source.header.Clone(),
	}, nil
}
