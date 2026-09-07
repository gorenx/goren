package registry

import (
	"errors"
	sessionactor "github.com/gorenx/goren/session/actor"
	"reflect"

	"github.com/gorenx/goren/session"
	goakt "github.com/tochemey/goakt/v4/actor"
)

func (owner *RegistryGrain) registerSession(input RegisterSession) error {
	if input.Storage == nil {
		return errors.New("session: registration storage is required")
	}
	if input.Restore && !reflect.DeepEqual(input.Options, session.CreateOptions{}) {
		return errors.New("session: recovery registration must not include creation options")
	}
	var message any
	if input.Restore {
		message = sessionactor.Resume{
			CreationID: input.CreationID,
			Storage:    input.Storage,
		}
	} else {
		message = sessionactor.Initialize{
			CreationID: input.CreationID,
			Options:    input.Options.Clone(),
			Storage:    input.Storage,
		}
	}
	if entry := owner.sessions[input.SessionID]; entry != nil && entry.closure != nil {
		return session.ErrWritesClosed
	}
	if err := owner.reserve(input.SessionID, input.CreationID); err != nil {
		return err
	}
	entry := owner.sessions[input.SessionID]
	if entry.initialization != nil {
		if !entry.initialization.matches(message) {
			return errors.New("session: registration conflicts with original initialization input")
		}
		return nil
	}
	entry.initialization = &initialization{
		message: message,
	}
	return nil
}

func (owner *RegistryGrain) startSession(gc *goakt.GrainContext, input StartSession) {
	if err := owner.directory.Check(input.SessionID, input.CreationID); err != nil {
		gc.Response(sessionactor.Initialized{
			Err: err,
		})
		return
	}
	entry := owner.sessions[input.SessionID]
	if entry == nil || entry.initialization == nil {
		gc.Response(sessionactor.Initialized{
			Err: errors.New("session: initialization input has not been registered"),
		})
		return
	}
	owner.openSession(gc, input.SessionID, input.CreationID, entry.initialization.message)
}
