package sessionapi

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/session/persistence"
)

// Agent exposes input admission and execution control for one initialized creation.
// Implementations retain its authority internally and reject requests after that
// creation is replaced. Session reads, titles and model settings have separate ports.
// A canceled or timed-out request does not prove that its mutation was rolled back.
type Agent interface {
	// ID returns the stable business identity shared by the Agent and its Session.
	ID() session.SessionID

	// Role identifies the admitted Agent capability without inferring it from ancestry.
	Role() agent.Role

	// ParentID returns the parent of this live creation, or nil for a root Agent.
	// It does not infer ownership from the Session's historical origin.
	ParentID() *session.SessionID

	// Pending returns detached next-turn and next-step inputs without claiming them.
	Pending(context.Context) (agent.Pending, error)

	// Status observes execution activity without waiting for the Agent to become idle.
	Status(context.Context) (agent.Status, error)

	// Followup commits input for a new turn and requests execution.
	// Success acknowledges admission, not completion of the resulting turn.
	Followup(context.Context, agentmessage.UserMessage) error

	// Steer commits input at the next admissible execution boundary. Agent policy
	// may place it in the next turn when the active turn cannot accept more input.
	Steer(context.Context, agentmessage.UserMessage) error

	// Replace commits replacement content under an existing pending message ID.
	// agent.ErrInputMissing means the message is no longer queued, including when execution claimed it.
	Replace(context.Context, agentmessage.MessageID, agentmessage.UserMessage) error

	// Remove commits cancellation of one pending occurrence. It does not retract
	// input already consumed by a turn; an absent message returns agent.ErrInputMissing.
	Remove(context.Context, agentmessage.MessageID) error

	// Promote atomically removes a next-turn occurrence and reinserts it at the
	// boundary selected by steering policy. An inadmissible move returns an error.
	Promote(context.Context, agentmessage.MessageID) error

	// Cancel delivers business cancellation intent and applies the Inbox option.
	// Success does not mean execution has ended or resources have been released.
	Cancel(context.Context, agent.CancelCause, agent.CancelOptions) error
}

// Titles owns explicit Session title changes independently of Agent execution.
type Titles interface {
	// Rename validates and commits a user title, which pins automatic generation.
	// The result identifies this operation's Session event, not a later title read;
	// a successful memory commit does not by itself acknowledge persistence flush.
	Rename(context.Context, session.SessionID, string) (Title, error)
}

// Title is the committed result of a Session title mutation.
type Title struct {
	Text string
	Seq  int64
}

// Opening selects new creation or durable recovery; lifecycle is owned by Registry.
type Opening struct {
	ID      session.SessionID
	Restore bool
	Options session.CreateOptions
}

// Agents exposes Agent creation and lookup without exposing lifecycle internals.
// The lifecycle owner coordinates resources; callers do not start component actors.
type Agents interface {
	// Resolve waits for an existing initialization and returns its admitted Agent.
	// A nil Agent with nil error means no current creation; it never creates one.
	Resolve(context.Context, session.SessionID) (Agent, error)

	// Create initializes or restores the Session described by Opening and returns
	// an admitted Agent. Concurrent creators may receive the winning creation;
	// each caller must still validate its own CWD and preset adoption requirements.
	Create(context.Context, Opening) (Agent, error)
}

// DirectoryProvisioner owns the directory side effect before Session creation.
type DirectoryProvisioner interface {
	// EnsureDirectory makes the requested directory available or returns the
	// filesystem/policy failure. It does not create or register a Session.
	EnsureDirectory(string) error
}

// DirectoryProvisionerFunc adapts composition-root filesystem provisioning.
type DirectoryProvisionerFunc func(string) error

func (operation DirectoryProvisionerFunc) EnsureDirectory(path string) error { return operation(path) }

// activation checks API adoption rules without retaining lifecycle or model state.
type activation struct {
	agents      Agents
	sessions    LiveSessions
	history     History
	directories DirectoryProvisioner
}

func (access *activation) ordinary(ctx context.Context, identifier session.SessionID) (Agent, error) {
	subject, err := access.agents.Resolve(ctx, identifier)
	if err != nil {
		return nil, err
	}
	if subject == nil {
		inspection, err := access.history.Inspect(ctx, identifier)
		if err != nil {
			return nil, err
		}
		recordedRole, err := agent.RoleFromEvents(identifier, inspection.Events)
		if err != nil {
			return nil, err
		}
		if recordedRole == agent.CompanionRole {
			return nil, &SubagentOwnershipError{identifier: identifier}
		}
		subject, err = access.agents.Create(ctx, Opening{
			ID:      identifier,
			Restore: true,
		})
		if err != nil {
			return nil, err
		}
		if subject == nil {
			return nil, errors.New("session API: activation returned no Agent")
		}
		if subject.ID() != inspection.Header.ID {
			return nil, errors.New("session API: recovery returned another identity")
		}
	}
	if subject.ID() != identifier {
		return nil, errors.New("session API: resolution returned another identity")
	}
	if subject.ParentID() != nil && subject.Role() != agent.CompanionRole {
		return nil, &SubagentOwnershipError{
			identifier: identifier,
		}
	}
	return subject, nil
}

func (access *activation) ensure(ctx context.Context, identifier session.SessionID, cwd string, preset *string) (Agent, error) {
	subject, err := access.agents.Resolve(ctx, identifier)
	if err != nil {
		return nil, err
	}
	if subject == nil {
		opening := Opening{
			ID: identifier,
		}
		_, err := access.history.Inspect(ctx, identifier)
		if err == nil {
			opening.Restore = true
		} else {
			var missing *persistence.NotFoundError
			if !errors.As(err, &missing) {
				return nil, err
			}
			if !filepath.IsAbs(cwd) {
				return nil, fmt.Errorf("project directory %q must be absolute", cwd)
			}
			if err := access.directories.EnsureDirectory(cwd); err != nil {
				return nil, err
			}
			opening.Options.Metadata = session.Metadata{
				CWD:         &cwd,
				AgentPreset: cloneStringPointer(preset),
			}
		}
		subject, err = access.agents.Create(ctx, opening)
		if err != nil {
			// Another caller may have reserved the same ID. Registry waits for
			// that creation; each API request still checks its own adoption input.
			winner, resolveErr := access.agents.Resolve(ctx, identifier)
			if resolveErr != nil || winner == nil {
				return nil, err
			}
			subject = winner
		}
	}
	if subject == nil || subject.ID() != identifier {
		return nil, errors.New("session API: activation returned an invalid identity")
	}
	if subject.ParentID() != nil {
		return nil, &SubagentOwnershipError{
			identifier: identifier,
		}
	}
	view, err := access.sessions.Read(ctx, identifier)
	if err != nil {
		return nil, err
	}
	if view == nil {
		return nil, errors.New("session API: initialized Session is unavailable")
	}
	header := view.Header
	if preset != nil && (header.AgentPreset == nil || *header.AgentPreset != *preset) {
		return nil, &PresetConflictError{
			identifier: identifier,
			requested:  *preset,
			existing:   cloneStringPointer(header.AgentPreset),
		}
	}
	if header.CWD == nil || *header.CWD != cwd {
		return nil, &CWDConflictError{
			identifier: identifier,
			requested:  cwd,
			existing:   cloneStringPointer(header.CWD),
		}
	}
	return subject, nil
}
