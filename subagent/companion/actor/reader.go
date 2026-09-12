package companionactor

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agent"
	agentregistry "github.com/gorenx/goren/agent/registry"
	loopactor "github.com/gorenx/goren/agentloop/actor"
	companions "github.com/gorenx/goren/subagent/companion"
	"github.com/gorenx/goren/subagent/tools/control"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Reader observes Companion relationships and current activity for listing tools.
// It owns no directory, child lifetime, configuration or execution state.
type Reader struct {
	// system is borrowed for ordinary Registry, Steward and AgentLoop queries.
	system goakt.ActorSystem
	// registry owns exact Agent identity, parent relationships and admission.
	registry *goakt.GrainIdentity
}

// NewReader binds existing capabilities without creating any Agent or Steward.
func NewReader(system goakt.ActorSystem, registry *goakt.GrainIdentity) (*Reader, error) {
	if system == nil || registry == nil {
		return nil, companions.ErrInvalid
	}
	return &Reader{
		system:   system,
		registry: registry,
	}, nil
}

// List reads only the calling parent's direct Companions, with exact credentials.
func (observer *Reader) List(ctx context.Context, input control.Listing) ([]control.Entry, error) {
	if input.Scope != "children" {
		return nil, companions.ErrInvalid
	}
	parent, err := callerCreation(ctx, input.ParentSessionID)
	if err != nil {
		return nil, err
	}
	if err := observer.admit(ctx, parent); err != nil {
		return nil, err
	}
	target, err := stewardRoute(ctx, observer.system, parent)
	if err != nil {
		return nil, err
	}
	sender := &Client{
		system: observer.system,
		target: target,
		parent: parent,
	}
	views, err := sender.List(ctx)
	if err != nil {
		return nil, err
	}
	rows := make([]control.Entry, 0, len(views))
	for _, current := range views {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if current.Companion.Removed {
			continue
		}
		row := control.Entry{
			Kind:  "child",
			ID:    string(current.Companion.ChildSessionID),
			Label: current.Companion.Name,
		}
		phase := current.Phase
		if !current.Persisted {
			phase = Starting
			if current.Err != nil {
				phase = StartFailed
			}
		}
		switch phase {
		case Starting, Draining, Stopping, Stopped, StartFailed, CloseFailed:
			row.Status = string(phase)
		case Ready:
			observed, failure := observer.Activity(ctx, parent, current.Creation)
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if failure != nil {
				row.Kind = "diagnostic"
				row.Label = ""
				row.Reason = "unavailable"
			} else {
				row.Status = string(observed)
			}
		default:
			return nil, errors.New("companion: unknown lifecycle in listing")
		}
		rows = append(rows, row)
	}
	if err := observer.admit(ctx, parent); err != nil {
		return nil, err
	}
	return rows, nil
}

func (observer *Reader) admit(ctx context.Context, parent agentregistry.Creation) error {
	admitted, err := ask[agentregistry.Completion](ctx, observer.system, observer.registry, agentregistry.CheckAdmission{
		Creation: parent,
	})
	return errors.Join(err, admitted.Err)
}

// Activity reads the exact child lifetime and verifies its direct parent and role.
// Management and model-visible queries share this observation; neither owns execution state.
func (observer *Reader) Activity(ctx context.Context, parent, child agentregistry.Creation) (agent.Status, error) {
	if child.AgentID == "" || child.CreationID == "" {
		return "", agentregistry.ErrCreation
	}
	ready, err := ask[agentregistry.Ready](ctx, observer.system, observer.registry, agentregistry.FindAgent{
		AgentID: child.AgentID,
	})
	if err = errors.Join(err, ready.Err); err != nil {
		return "", err
	}
	if ready.Creation != child || ready.Executor == nil || ready.Registration.Role != agent.CompanionRole || ready.Registration.ParentID == nil || *ready.Registration.ParentID != parent.AgentID {
		return "", agentregistry.ErrCreation
	}
	observed, err := ask[loopactor.Activity](ctx, observer.system, ready.Executor, loopactor.ReadActivity{
		CreationID: child.CreationID,
	})
	if err = errors.Join(err, observed.Err); err != nil {
		return "", err
	}
	if observed.Status != agent.StatusIdle && observed.Status != agent.StatusRunning {
		return "", errors.New("companion: invalid activity in listing")
	}
	return observed.Status, nil
}

var _ control.Lister = (*Reader)(nil)
