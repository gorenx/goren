package registry

import (
	"context"
	"errors"

	sessionactor "github.com/gorenx/goren/session/actor"

	"github.com/gorenx/goren/session"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// RegistryName identifies the shared Session creation authority.
const RegistryName = "session-registry"

// RegistryGrain owns Session reservations and initialized routes, not Session logs.
// The composition root must create it as a long-lived Grain.
type RegistryGrain struct {
	directory authority
	// sessions maps each stable SessionID to the lifecycle work for its current
	// creation: fixed initialization input, pending close, or completed close result.
	// The value contains no Session log or Grain instance. A new creation replaces
	// the record; a closed record remains so duplicate close requests can be answered.
	sessions map[session.SessionID]*lifecycle
}

// OnActivate starts without authorizing any prior creation.
func (*RegistryGrain) OnActivate(context.Context, *goakt.GrainProps) error { return nil }

// OnDeactivate owns no external resources and does not close Session instances.
func (owner *RegistryGrain) OnDeactivate(context.Context, *goakt.GrainProps) error {
	for _, entry := range owner.sessions {
		if entry.initialization != nil {
			entry.initialization.respond(sessionactor.Initialized{
				Err: sessionactor.ErrUnavailable,
			})
		}
		if operation := entry.closure; operation != nil {
			if operation.cancel != nil {
				operation.cancel()
			}
			operation.respond(sessionactor.ErrUnavailable)
		}
	}
	owner.sessions = nil
	return nil
}

// OnReceive serializes reservation changes and asynchronously coordinates Session lifecycle.
func (owner *RegistryGrain) OnReceive(gc *goakt.GrainContext) {
	var err error
	switch incoming := gc.Message().(type) {
	case ResolveSession:
		gc.Response(owner.resolveSession(incoming))
		return
	case FindSession:
		gc.Response(owner.findSession(incoming))
		return
	case ListSessions:
		gc.Response(owner.listSessions())
		return
	case RegisterSession:
		err = owner.registerSession(incoming)
	case StartSession:
		owner.startSession(gc, incoming)
		return
	case CreateSession:
		owner.createSession(gc, incoming)
		return
	case ResumeSession:
		owner.resumeSession(gc, incoming)
		return
	case CloseSession:
		owner.closeSession(gc, incoming)
		return
	case closureReady:
		owner.continueClosure(gc, incoming)
		return
	case closureFinished:
		owner.finishClosure(gc, incoming)
		return
	case Reserve:
		err = owner.reserve(incoming.SessionID, incoming.CreationID)
	case sessionactor.Claim:
		gc.Response(sessionactor.Claimed{
			Err: owner.directory.Claim(incoming.SessionID, incoming.CreationID, incoming.InstanceID),
		})
		return
	case Retire:
		err = owner.directory.Retire(incoming.SessionID, incoming.CreationID)
	case Release:
		if entry := owner.sessions[incoming.SessionID]; entry != nil && (entry.closure != nil || entry.initialization != nil) {
			err = errors.New("session: closure owns reservation release")
		} else {
			err = owner.directory.Release(incoming.SessionID, incoming.CreationID)
		}
	default:
		gc.Unhandled()
		return
	}
	gc.Response(Completion{
		Err: err,
	})
}

func (owner *RegistryGrain) reserve(identifier session.SessionID, creationID string) error {
	previous := owner.sessions[identifier]
	if previous != nil && previous.result != nil && previous.creationID == creationID {
		return errors.New("session: completed creation cannot be reserved again")
	}
	if err := owner.directory.Reserve(identifier, creationID); err != nil {
		return err
	}
	if previous == nil || previous.creationID != creationID {
		if owner.sessions == nil {
			owner.sessions = make(map[session.SessionID]*lifecycle)
		}
		owner.sessions[identifier] = &lifecycle{
			creationID: creationID,
		}
	}
	return nil
}
