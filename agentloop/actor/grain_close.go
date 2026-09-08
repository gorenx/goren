package loopactor

import (
	"errors"

	agentactor "github.com/gorenx/goren/agent/actor"
	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	sessionregistry "github.com/gorenx/goren/session/registry"
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
	if failure != nil || owner.sessionRegistry == nil {
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
	if owner.prompt != nil {
		owner.closePrompt(gc, reply)
		return
	}
	owner.closeSession(gc, reply)
}

func (owner *Grain) closeSession(gc *goakt.GrainContext, reply *goakt.GrainReply) {
	gc.RequestGrain(owner.sessionRegistry, sessionregistry.CloseSession{
		SessionID:  session.SessionID(gc.Self().Name()),
		CreationID: owner.creationID,
	}, goakt.WithRequestTimeout(0)).Then(func(payload any, failure error) {
		if failure == nil {
			result, ok := payload.(sessionactor.Closed)
			if !ok {
				failure = errors.New("agentloop: invalid Session close response")
			} else {
				failure = result.Err
			}
		}
		if failure == nil {
			failure = owner.business.Release()
		}
		if failure == nil {
			owner.conversation = nil
			owner.sessionRegistry = nil
		}
		reply.Response(agentactor.Closed{
			Err: failure,
		})
	})
}
