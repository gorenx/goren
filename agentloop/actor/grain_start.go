package loopactor

import (
	"errors"

	sessionactor "github.com/gorenx/goren/session/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

// startTurn commits a previously accepted execution's Turn boundary.
// Retrying uses the same idempotent write and receives its original commitment.
type startTurn struct{}
type turnStarted struct {
	opened bool
	err    error
}

type turnOpening struct {
	reply *goakt.GrainReply
}

type confirmTurnStart struct {
	request *turnOpening
	write   sessionactor.Committed
}

func (owner *Grain) startTurn(gc *goakt.GrainContext) {
	if owner.opening != nil || owner.preparation != nil || owner.model != nil || owner.stopping != nil || owner.ending != nil || owner.conversation == nil {
		gc.Response(turnStarted{
			err: errors.New("agentloop: Turn start requires released transport and Session binding"),
		})
		return
	}
	reply := gc.DeferResponse()
	if reply == nil {
		gc.Response(turnStarted{
			err: errors.New("agentloop: Turn start requires an asynchronous request"),
		})
		return
	}
	request := &turnOpening{
		reply: reply,
	}
	owner.opening = request
	draft, err := owner.business.PrepareTurn()
	if err != nil {
		owner.finishTurnStart(request, false, err)
		return
	}
	completed := make(chan confirmTurnStart, 1)
	if !owner.awaitTurnStart(gc, request, completed) {
		return
	}
	gc.RequestGrain(owner.conversation, sessionactor.Commit{
		CreationID: owner.creationID,
		Plan: turnStartWrite{
			event: draft,
		},
	}, goakt.WithRequestTimeout(0), goakt.WithReentrancyMode(reentrancy.StashNonReentrant)).Then(func(payload any, failure error) {
		write, ok := payload.(sessionactor.Committed)
		if failure != nil {
			write.Err = failure
		} else if !ok {
			write.Err = errors.New("agentloop: invalid Turn start response")
		}
		completed <- confirmTurnStart{
			request: request,
			write:   write,
		}
	})
}

func (owner *Grain) awaitTurnStart(gc *goakt.GrainContext, request *turnOpening, completed <-chan confirmTurnStart) bool {
	stopped := owner.stopped
	if err := gc.PipeToSelf(func() (any, error) {
		select {
		case result := <-completed:
			return result, nil
		case <-stopped:
			return nil, nil
		}
	}); err != nil {
		owner.finishTurnStart(request, false, err)
		return false
	}
	return true
}

func (owner *Grain) confirmTurnStart(gc *goakt.GrainContext, input confirmTurnStart) {
	if owner.opening != input.request {
		return
	}
	failure := input.write.Err
	if failure == nil {
		failure = owner.business.ConfirmTurn(input.write.Result)
	}
	owner.finishTurnStart(input.request, failure == nil, failure)
}

func (owner *Grain) finishTurnStart(request *turnOpening, opened bool, failure error) {
	if owner.opening != request {
		return
	}
	request.reply.Response(turnStarted{
		opened: opened,
		err:    failure,
	})
	owner.opening = nil
}
