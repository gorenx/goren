package registry

import (
	"context"
	"errors"
	sessionactor "github.com/gorenx/goren/session/actor"

	"github.com/google/uuid"
	"github.com/gorenx/goren/session"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

// closure retains the successful business close across framework-stop retries.
// replies represent callers of this one use case, not a business message queue.
type closure struct {
	closed *sessionactor.Closed
	// replies holds deferred GoAkt replies for callers sharing this close operation.
	// Concurrent CloseSession requests append their reply handles instead of starting
	// another close. Success replies only after business drain, Grain deactivation,
	// and reservation release; failure replies with the error to every waiting caller.
	// respond clears these handles after either outcome. A failed operation retains
	// its progress, and a later retry supplies new reply handles.
	replies []*goakt.GrainReply
	cancel  context.CancelFunc
	token   string
}

type closureFinished struct {
	sessionID session.SessionID
	token     string
	err       error
}

func (owner *RegistryGrain) closeSession(gc *goakt.GrainContext, input CloseSession) {
	entry := owner.sessions[input.SessionID]
	if entry != nil && entry.result != nil && entry.creationID == input.CreationID {
		gc.Response(*entry.result)
		return
	}
	if err := owner.directory.Check(input.SessionID, input.CreationID); err != nil {
		gc.Response(sessionactor.Closed{
			Err: err,
		})
		return
	}
	if err := gc.EnableReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))); err != nil {
		gc.Response(sessionactor.Closed{
			Err: err,
		})
		return
	}
	reply := gc.DeferResponse()
	if reply == nil {
		gc.Response(sessionactor.Closed{
			Err: errors.New("session: registry close requires an asynchronous request"),
		})
		return
	}
	operation := entry.closure
	if operation == nil {
		operation = &closure{}
		entry.closure = operation
	}
	operation.replies = append(operation.replies, reply)
	if operation.cancel != nil {
		return
	}
	if pending := entry.initialization; pending != nil && pending.settled != nil {
		owner.waitForInitialization(gc, input, operation, pending.settled)
		return
	}
	owner.runClosure(gc, input, operation)
}

func (owner *RegistryGrain) runClosure(gc *goakt.GrainContext, input CloseSession, operation *closure) {
	system := gc.ActorSystem()
	identity, err := goakt.GrainOf[*sessionactor.Grain](gc.Context(), system, string(input.SessionID), goakt.WithLongLivedGrain())
	if err != nil {
		operation.respond(err)
		return
	}
	ctx, cancel := context.WithCancel(context.WithoutCancel(gc.Context()))
	token := uuid.NewString()
	operation.token = token
	operation.cancel = cancel
	authorized := make(chan error, 1)
	// Framework TellGrain waits for target processing, so it runs in a managed
	// task after the business close succeeds and authority is revoked on-turn.
	err = gc.PipeToSelf(func() (any, error) {
		var failure error
		select {
		case failure = <-authorized:
			if failure == nil {
				failure = system.TellGrain(ctx, identity, new(goakt.PoisonPill))
			}
		case <-ctx.Done():
			failure = context.Cause(ctx)
		}
		return closureFinished{
			sessionID: input.SessionID,
			token:     token,
			err:       failure,
		}, nil
	})
	if err != nil {
		cancel()
		operation.cancel = nil
		operation.respond(err)
		return
	}
	if operation.closed != nil {
		authorized <- nil
		return
	}
	gc.RequestGrain(identity, sessionactor.Close{
		Authority:  gc.Self(),
		CreationID: input.CreationID,
	}).Then(func(payload any, failure error) {
		if failure == nil {
			result, ok := payload.(sessionactor.Closed)
			switch {
			case !ok:
				failure = errors.New("session: invalid business close reply")
			case result.Err != nil:
				failure = result.Err
			case result.Barrier.SessionID != input.SessionID || result.Barrier.NextSeq < 0:
				failure = errors.New("session: invalid business close barrier")
			default:
				failure = owner.directory.Retire(input.SessionID, input.CreationID)
				if failure == nil {
					operation.closed = &result
				}
			}
		}
		authorized <- failure
	})
}

func (owner *RegistryGrain) finishClosure(gc *goakt.GrainContext, result closureFinished) {
	entry := owner.sessions[result.sessionID]
	if entry == nil {
		return
	}
	operation := entry.closure
	if operation == nil || operation.token != result.token {
		return
	}
	operation.cancel()
	operation.cancel = nil
	failure := result.err
	if failure == nil {
		failure = owner.directory.Release(result.sessionID, entry.creationID)
	}
	if failure == nil {
		entry.result = operation.closed
		entry.closure = nil
		entry.initialization = nil
		if topic := gc.ActorSystem().TopicActor(); topic != nil {
			if err := goakt.Tell(gc.Context(), topic, goakt.NewPublish(uuid.NewString(), sessionactor.ClosedTopic, sessionactor.ClosedNotice{
				SessionID:  result.sessionID,
				CreationID: entry.creationID,
				Barrier:    entry.result.Barrier,
			})); err != nil {
				gc.ActorSystem().Logger().Errorf("session: close notification failed: %v", err)
			}
		}
	}

	operation.respond(failure)
}

func (operation *closure) respond(failure error) {
	result := sessionactor.Closed{
		Err: failure,
	}
	if operation.closed != nil {
		result.Barrier = operation.closed.Barrier
	}
	for _, reply := range operation.replies {
		reply.Response(result)
	}
	operation.replies = nil
}
