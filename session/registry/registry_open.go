package registry

import (
	"errors"
	sessionactor "github.com/gorenx/goren/session/actor"
	"reflect"

	"github.com/gorenx/goren/session"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

// initialization fixes the original input and tracks requests, including retries.
// message is a private framework envelope: only Initialize or Resume is stored.
type initialization struct {
	message any
	replies []*goakt.GrainReply
	// settled is closed when this initialization attempt returns, including failure.
	settled chan struct{}
}

func (owner *RegistryGrain) createSession(gc *goakt.GrainContext, input CreateSession) {
	if input.Storage == nil {
		gc.Response(sessionactor.Initialized{
			Err: errors.New("session: storage is required"),
		})
		return
	}
	owner.openSession(gc, input.SessionID, input.CreationID, sessionactor.Initialize{
		CreationID: input.CreationID,
		Options:    input.Options.Clone(),
		Storage:    input.Storage,
	})
}

func (owner *RegistryGrain) resumeSession(gc *goakt.GrainContext, input ResumeSession) {
	if input.Storage == nil {
		gc.Response(sessionactor.Initialized{
			Err: errors.New("session: recovery storage is required"),
		})
		return
	}
	owner.openSession(gc, input.SessionID, input.CreationID, sessionactor.Resume{
		CreationID: input.CreationID,
		Storage:    input.Storage,
	})
}

func (owner *RegistryGrain) openSession(gc *goakt.GrainContext, identifier session.SessionID, creationID string, message any) {
	if entry := owner.sessions[identifier]; entry != nil && entry.closure != nil {
		gc.Response(sessionactor.Initialized{
			Err: session.ErrWritesClosed,
		})
		return
	}
	if err := owner.reserve(identifier, creationID); err != nil {
		gc.Response(sessionactor.Initialized{
			Err: err,
		})
		return
	}
	entry := owner.sessions[identifier]
	operation := entry.initialization
	if operation != nil && !operation.matches(message) {
		gc.Response(sessionactor.Initialized{
			Err: errors.New("session: initialization input conflicts with reserved creation"),
		})
		return
	}
	if err := gc.EnableReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))); err != nil {
		gc.Response(sessionactor.Initialized{
			Err: err,
		})
		return
	}
	reply := gc.DeferResponse()
	if reply == nil {
		gc.Response(sessionactor.Initialized{
			Err: errors.New("session: registry initialization requires an asynchronous request"),
		})
		return
	}
	if operation == nil {
		operation = &initialization{
			message: message,
		}
		entry.initialization = operation
	}
	operation.replies = append(operation.replies, reply)
	if len(operation.replies) > 1 {
		return
	}
	operation.settled = make(chan struct{})
	identity, err := goakt.GrainOf[*sessionactor.Grain](gc.Context(), gc.ActorSystem(), string(identifier), goakt.WithLongLivedGrain())
	if err != nil {
		operation.respond(sessionactor.Initialized{
			Err: err,
		})
		return
	}
	outgoing := operation.message
	switch input := outgoing.(type) {
	case sessionactor.Initialize:
		input.Authority = gc.Self()
		outgoing = input
	case sessionactor.Resume:
		input.Authority = gc.Self()
		outgoing = input
	}
	gc.RequestGrain(identity, outgoing, goakt.WithRequestTimeout(0)).Then(
		func(payload any, failure error) {
			result := sessionactor.Initialized{
				Err: failure,
			}
			if failure == nil {
				initialized, ok := payload.(sessionactor.Initialized)
				switch {
				case !ok:
					result.Err = errors.New("session: invalid initialization reply")
				case initialized.Err != nil:
					result.Err = initialized.Err
				case initialized.Header.ID != identifier || initialized.Barrier.SessionID != identifier:
					result.Err = errors.New("session: initialization returned a different Session")
				default:
					result = initialized
				}
			}
			if result.Err == nil && owner.sessions[identifier] == entry {
				header := result.Header.Clone()
				entry.header = &header
			}
			operation.respond(result)
		},
	)
}

func (operation *initialization) matches(message any) bool {
	switch original := operation.message.(type) {
	case sessionactor.Initialize:
		candidate, ok := message.(sessionactor.Initialize)
		return ok && reflect.DeepEqual(original.Options, candidate.Options)
	case sessionactor.Resume:
		_, ok := message.(sessionactor.Resume)
		return ok
	default:
		return false
	}
}

func (operation *initialization) respond(result sessionactor.Initialized) {
	for _, reply := range operation.replies {
		// Header contains pointer fields. Each caller receives independent values.
		detached := result
		detached.Header = result.Header.Clone()
		reply.Response(detached)
	}
	operation.replies = nil
	if operation.settled != nil {
		close(operation.settled)
		operation.settled = nil
	}
}
