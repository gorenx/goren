package loopactor

import (
	"context"
	"errors"
	sessionactor "github.com/gorenx/goren/session/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

type finishTurn struct {
	problem error
}

type turnFinished struct {
	err error
}

// turnClosure retains only the in-flight request and mailbox task cancellation.
type turnClosure struct {
	reply  *goakt.GrainReply
	cancel context.CancelFunc
}

type turnProgress struct {
	operation *turnClosure
	payload   any
	err       error
}

func (owner *Grain) settleTurn(gc *goakt.GrainContext, input finishTurn) {
	if owner.ending != nil && owner.ending.reply != nil {
		gc.Response(turnFinished{
			err: errors.New("agentloop: Turn settlement request is already pending"),
		})
		return
	}
	if owner.conversation == nil || owner.model != nil {
		gc.Response(turnFinished{
			err: errors.New("agentloop: Turn settlement requires Session binding and released model resources"),
		})
		return
	}
	reply := gc.DeferResponse()
	if reply == nil {
		gc.Response(turnFinished{
			err: errors.New("agentloop: Turn settlement requires an asynchronous request"),
		})
		return
	}
	if owner.ending == nil {
		owner.ending = &turnClosure{}
	}
	operation := owner.ending
	operation.reply = reply
	if _, err := owner.business.TurnBarrier(); err == nil {
		owner.flushTurnEnd(gc, operation)
		return
	}
	ending, err := owner.business.PrepareTurnEnd(input.problem)
	if err != nil {
		owner.finishTurnRequest(operation, err)
		return
	}
	owner.requestTurnEnd(gc, operation, sessionactor.Commit{
		CreationID: owner.creationID,
		Plan: turnEndWrite{
			event: ending,
		},
	})
}

func (owner *Grain) flushTurnEnd(gc *goakt.GrainContext, operation *turnClosure) {
	barrier, err := owner.business.TurnBarrier()
	if err != nil {
		owner.finishTurnRequest(operation, err)
		return
	}
	owner.requestTurnEnd(gc, operation, sessionactor.Flush{
		CreationID: owner.creationID,
		Barrier:    barrier,
	})
}

func (owner *Grain) requestTurnEnd(gc *goakt.GrainContext, operation *turnClosure, message any) {
	ctx, cancel := context.WithCancel(context.WithoutCancel(gc.Context()))
	operation.cancel = cancel
	completed := make(chan turnProgress, 1)
	if err := gc.PipeToSelf(func() (any, error) {
		select {
		case result := <-completed:
			return result, nil
		case <-ctx.Done():
			return turnProgress{
				operation: operation,
				err:       context.Cause(ctx),
			}, nil
		}
	}); err != nil {
		owner.finishTurnRequest(operation, err)
		return
	}
	gc.RequestGrain(owner.conversation, message,
		goakt.WithRequestTimeout(0),
		goakt.WithReentrancyMode(reentrancy.StashNonReentrant),
	).Then(func(payload any, failure error) {
		if failure == nil {
			valid := false
			switch message.(type) {
			case sessionactor.Commit:
				_, valid = payload.(sessionactor.Committed)
			case sessionactor.Read:
				_, valid = payload.(sessionactor.View)
			case sessionactor.Flush:
				_, valid = payload.(sessionactor.Flushed)
			}
			if !valid {
				failure = errors.New("agentloop: unexpected Turn settlement response")
			}
		}
		completed <- turnProgress{
			operation: operation,
			payload:   payload,
			err:       failure,
		}
	})
}

func (owner *Grain) advanceTurnEnd(gc *goakt.GrainContext, input turnProgress) {
	operation := input.operation
	if owner.ending != operation || operation.reply == nil {
		return
	}
	operation.cancel()
	operation.cancel = nil
	if input.err != nil {
		owner.finishTurnRequest(operation, input.err)
		return
	}
	switch result := input.payload.(type) {
	case sessionactor.Committed:
		if result.Err != nil {
			owner.finishTurnRequest(operation, result.Err)
			return
		}
		if err := owner.business.ConfirmTurnEnd(result.Result); err != nil {
			owner.finishTurnRequest(operation, err)
			return
		}
		if _, err := owner.business.TurnBarrier(); err != nil {
			owner.requestTurnEnd(gc, operation, sessionactor.Read{
				CreationID: owner.creationID,
			})
			return
		}
		owner.flushTurnEnd(gc, operation)
	case sessionactor.View:
		if result.Err != nil {
			owner.finishTurnRequest(operation, result.Err)
			return
		}
		if err := owner.business.ReconcileTurnEnd(result.Snapshot); err != nil {
			owner.finishTurnRequest(operation, err)
			return
		}
		owner.flushTurnEnd(gc, operation)
	case sessionactor.Flushed:
		if result.Err != nil {
			owner.finishTurnRequest(operation, result.Err)
			return
		}
		owner.finishTurnRequest(operation, owner.business.ConfirmTurnFlush(result.Barrier))
	default:
		owner.finishTurnRequest(operation, errors.New("agentloop: unexpected Turn settlement response"))
	}
}

func (owner *Grain) finishTurnRequest(operation *turnClosure, failure error) {
	defer owner.refreshIdle()
	if owner.ending != operation {
		return
	}
	if operation.cancel != nil {
		operation.cancel()
		operation.cancel = nil
	}
	if operation.reply != nil {
		operation.reply.Response(turnFinished{
			err: failure,
		})
		operation.reply = nil
	}
	if failure == nil {
		owner.ending = nil
	}
}
