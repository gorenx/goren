package projectionactor

import (
	"context"
	"errors"
	sessionactor "github.com/gorenx/goren/session/actor"

	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/session/projection"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Grain serializes registration and projection computations without performing I/O.
// Composition keeps this Grain long-lived and registers Units before admitting work.
type Grain struct {
	registry *projection.Registry
	// projections maps an internal Session creation to its independent business fold.
	// A present nil value retains closure knowledge and rejects arbitrarily late notices.
	projections map[creation]*projection.Projection
}

// OnActivate creates an empty registry of rebuildable state.
func (owner *Grain) OnActivate(context.Context, *goakt.GrainProps) error {
	owner.registry = projection.NewRegistry()
	owner.projections = make(map[creation]*projection.Projection)
	return nil
}

// OnDeactivate drops rebuildable state; no Session or storage is owned here.
func (owner *Grain) OnDeactivate(context.Context, *goakt.GrainProps) error {
	owner.registry = nil
	owner.projections = nil
	return nil
}

// OnReceive responds directly to pure computations; callers use asynchronous requests.
func (owner *Grain) OnReceive(gc *goakt.GrainContext) {
	switch input := gc.Message().(type) {
	case Register:
		id, err := owner.registry.Register(input.Unit)
		gc.Response(Registered{
			ID:  id,
			Err: err,
		})
	case Remove:
		owner.registry.Remove(input.ID)
		gc.Response(Completion{})
	case Read:
		current, err := owner.projection(input.SessionID, input.CreationID)
		var snapshot projection.Snapshot
		if err == nil {
			snapshot, err = current.Snapshot(input.Events)
		}
		gc.Response(Viewed{
			Snapshot: snapshot,
			Err:      err,
		})
	case Capture:
		current, err := owner.projection(input.SessionID, input.CreationID)
		var rows projection.Checkpoint
		if err == nil {
			rows, err = current.Checkpoint(input.Events)
		}
		gc.Response(Captured{
			Checkpoint: rows,
			Err:        err,
		})
	case sessionactor.CommitNotice:
		owner.appended(gc, input)
	case sessionactor.ClosedNotice:
		owner.projections[creation{
			sessionID: input.SessionID,
			id:        input.CreationID,
		}] = nil
		gc.Response(Completion{})
	case Forget:
		owner.projections[creation{
			sessionID: input.SessionID,
			id:        input.CreationID,
		}] = nil
		gc.Response(Completion{})
	case RestoreFloor:
		gc.Response(Floor{
			Seq: owner.registry.RestoreFloor(input.Checkpoint),
		})
	case ViewCheckpoint:
		values, err := owner.registry.ViewCheckpoint(input.Checkpoint)
		gc.Response(Values{
			Values: values,
			Err:    err,
		})
	case Restore:
		result, err := owner.registry.Restore(input.Checkpoint, input.Events, input.BaseSeq)
		gc.Response(Restored{
			Result: result,
			Err:    err,
		})
	default:
		gc.Unhandled()
	}
}

// creation is an internal routing key, never a business or persistence value.
type creation struct {
	sessionID session.SessionID
	// id is the CreationID assigned to one Session lifecycle, not its persistent
	// SessionID or a Grain activation ID. Paired with sessionID, it keeps delayed
	// commit and close notices from an earlier creation away from the new fold.
	// For example, reopening Session "chat" under creation B must not let a late
	// close notice for creation A release B's projection.
	id string
}

func (owner *Grain) projection(identifier session.SessionID, creationID string) (*projection.Projection, error) {
	if identifier == "" || creationID == "" {
		return nil, errors.New("sessionprojection: Session and creation identities are required")
	}
	key := creation{
		sessionID: identifier,
		id:        creationID,
	}
	current, found := owner.projections[key]
	if found && current == nil {
		return nil, errors.New("sessionprojection: creation has been released")
	}
	if !found {
		current = owner.registry.NewProjection()
		owner.projections[key] = current
	}
	return current, nil
}
