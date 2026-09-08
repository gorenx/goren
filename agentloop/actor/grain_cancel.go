package loopactor

import (
	"errors"
	"github.com/gorenx/goren/agent"
	middlewareactor "github.com/gorenx/goren/agent/middleware/actor"
	promptactor "github.com/gorenx/goren/systemprompt/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
)

func (owner *Grain) cancel(gc *goakt.GrainContext, input Cancel) {
	reply := owner.inboxReply(gc, input.CreationID)
	if reply == nil {
		return
	}
	edit, err := owner.business.Cancel(input.Cause, input.Options)
	var deliveryErr error
	if owner.preparation != nil {
		operation := owner.preparation
		deliveryErr = gc.ActorSystem().TellGrain(gc.Context(), owner.prompt, promptactor.CancelAssembly{
			ID: operation.id,
		})
		deliveryErr = errors.Join(deliveryErr, gc.ActorSystem().TellGrain(gc.Context(), operation.middleware, middlewareactor.CancelCall{
			CallID: middlewareactor.CallID(operation.id),
			Cause:  agent.NewCancellation(input.Cause),
		}))
	}
	if owner.model != nil {
		owner.model.cancelRead(agent.NewCancellation(input.Cause))
	}
	deliveryErr = errors.Join(err, deliveryErr)
	if len(edit.Drafts) == 0 {
		reply.Response(Changed{
			Err: deliveryErr,
		})
		return
	}
	owner.commitInbox(gc, reply, agent.Admission{
		Edit: edit,
	}, deliveryErr)
}
