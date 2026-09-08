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

// Agent is the Session API's runtime port for one initialized creation.
// Implementations retain that creation's authority, never mutable business objects.
type Agent interface {
	ID() session.SessionID
	Header() session.Header
	ParentID() *session.SessionID
	Snapshot(context.Context) (Snapshot, error)
	Pending(context.Context) (agent.Pending, error)
	Followup(context.Context, agentmessage.UserMessage) error
	Steer(context.Context, agentmessage.UserMessage) error
	Replace(context.Context, agentmessage.MessageID, agentmessage.UserMessage) error
	Remove(context.Context, agentmessage.MessageID) error
	Promote(context.Context, agentmessage.MessageID) error
	Cancel(context.Context, agent.CancelCause, agent.CancelOptions) error
	Rename(context.Context, string) (Title, error)
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

// Agents resolves ready creations and submits lifecycle requests to their owner.
// Resolve waits for an existing initialization; nil means no current creation.
type Agents interface {
	Resolve(context.Context, session.SessionID) (Agent, error)
	Create(context.Context, Opening) (Agent, error)
}

// DirectoryProvisioner owns the directory side effect before Session creation.
type DirectoryProvisioner interface{ EnsureDirectory(string) error }

// DirectoryProvisionerFunc adapts composition-root filesystem provisioning.
type DirectoryProvisionerFunc func(string) error

func (operation DirectoryProvisionerFunc) EnsureDirectory(path string) error { return operation(path) }

// activation checks API adoption rules without retaining lifecycle or model state.
type activation struct {
	agents      Agents
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
	if subject.ParentID() != nil {
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
	header := subject.Header()
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
