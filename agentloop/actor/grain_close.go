package loopactor

import (
	"errors"

	agentactor "github.com/gorenx/goren/agent/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
)

func (owner *Grain) checkCreation(identifier string) error {
	if identifier == "" || owner.creationID != identifier {
		return errors.New("agentloop: creation is not authorized or current")
	}
	return nil
}

func (owner *Grain) prepareClose(identifier string) error {
	if err := owner.checkCreation(identifier); err != nil {
		return err
	}
	if owner.initialization != nil {
		return errors.New("agentloop: initialization has not finished")
	}
	return owner.business.PrepareClose(owner.model != nil && owner.model.reply != nil)
}

func (owner *Grain) closeResources(gc *goakt.GrainContext, input agentactor.CloseResources) {
	failure := owner.checkCreation(input.CreationID)
	if failure == nil && owner.initialization != nil {
		failure = errors.New("agentloop: initialization has not finished")
	}
	if failure == nil {
		failure = owner.business.CheckClose(owner.model != nil && owner.model.reply != nil)
	}
	if failure != nil {
		gc.Response(agentactor.Closed{
			Err: failure,
		})
		return
	}
	reply := gc.DeferResponse()
	if reply == nil {
		gc.Response(agentactor.Closed{
			Err: errors.New("agentloop: resource close requires an asynchronous request"),
		})
		return
	}
	if owner.model != nil {
		owner.model.cleanup = reply
		next, err := owner.business.RetryRequestClose()
		if err != nil {
			owner.finishModel(owner.model, err)
			return
		}
		owner.executeRequest(gc, owner.model, next)
		return
	}
	owner.releaseResources(reply)
}

func (owner *Grain) releaseResources(reply *goakt.GrainReply) {
	failure := owner.business.Release()
	if failure == nil {
		owner.conversation = nil
		owner.prompt = nil
	}
	reply.Response(agentactor.Closed{
		Err: failure,
	})
}
