package commandactor

import (
	"context"
	"errors"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

func (owner *Grain) cancel(gc *goakt.GrainContext, input Cancel) {
	if input.CreationID == "" || input.CreationID != owner.binding.CreationID {
		gc.Response(Cancelled{
			Err: errors.New("commands: creation is not current"),
		})
		return
	}
	current := owner.calls[input.CommandID]
	if current == nil {
		gc.Response(Cancelled{})
		return
	}
	cause := input.Cause
	if cause == nil {
		cause = context.Canceled
	}
	if current.cause == nil {
		current.cause = cause
	}
	if current.business != nil {
		if err := current.business.Cancel(current.cause); err == nil {
			owner.writeDone(gc, current)
		}
	}
	if !current.handlerPending {
		gc.Response(Cancelled{})
		return
	}
	reply := gc.DeferResponse()
	gc.RequestGrain(current.handler, CancelInvocation{
		CreationID: owner.binding.CreationID,
		CommandID:  input.CommandID,
		Cause:      current.cause,
	}, goakt.WithRequestTimeout(owner.binding.Timeout), goakt.WithReentrancyMode(reentrancy.AllowAll)).Then(func(payload any, failure error) {
		result, ok := payload.(Cancelled)
		if failure == nil && !ok {
			failure = errors.New("commands: invalid cancellation reply")
		}
		reply.Response(Cancelled{
			Err: errors.Join(failure, result.Err),
		})
	})
}
