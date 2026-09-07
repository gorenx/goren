package registry

import (
	"context"

	"github.com/google/uuid"
	"github.com/gorenx/goren/session"
	goakt "github.com/tochemey/goakt/v4/actor"
)

type closureReady struct {
	sessionID session.SessionID
	token     string
	err       error
}

func (owner *RegistryGrain) waitForInitialization(gc *goakt.GrainContext, input CloseSession, operation *closure, settled <-chan struct{}) {
	ctx, cancel := context.WithCancel(context.WithoutCancel(gc.Context()))
	token := uuid.NewString()
	operation.token = token
	operation.cancel = cancel
	if err := gc.PipeToSelf(func() (any, error) {
		var failure error
		select {
		case <-settled:
		case <-ctx.Done():
			failure = context.Cause(ctx)
		}
		return closureReady{
			sessionID: input.SessionID,
			token:     token,
			err:       failure,
		}, nil
	}); err != nil {
		cancel()
		operation.cancel = nil
		operation.respond(err)
	}
}

func (owner *RegistryGrain) continueClosure(gc *goakt.GrainContext, result closureReady) {
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
	if result.err != nil {
		operation.respond(result.err)
		return
	}
	owner.runClosure(gc, CloseSession{
		SessionID:  result.sessionID,
		CreationID: entry.creationID,
	}, operation)
}
