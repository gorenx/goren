package loopactor

import (
	"errors"

	agentactor "github.com/gorenx/goren/agent/actor"
	"github.com/gorenx/goren/agentloop"
	goakt "github.com/tochemey/goakt/v4/actor"
)

func (owner *Grain) checkCreation(identifier string) error {
	if identifier == "" || owner.creationID != identifier {
		return errors.New("agentloop: creation is not authorized or current")
	}
	return nil
}

func (owner *Grain) prepareClose(identifier string) error {
	err := owner.checkCreation(identifier)
	if err != nil {
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
		gc.Response(agentactor.Closed{Err: failure})
		return
	}
	reply := gc.DeferResponse()
	if reply == nil {
		gc.Response(agentactor.Closed{Err: errors.New("agentloop: resource close requires an asynchronous request")})
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
	reply.Response(agentactor.Closed{Err: failure})
}

// closePreparation retains a close reply while accepted execution settles.
// It uses the existing idle notification; it neither queues business commands
// nor turns cancellation acknowledgement into execution completion.
type closePreparation struct {
	creationID string
	reply      *goakt.GrainReply
}

func (owner *Grain) requestClosePreparation(gc *goakt.GrainContext, input agentactor.PrepareClose) {
	failure := owner.prepareClose(input.CreationID)
	if !errors.Is(failure, agentloop.ErrPending) || !owner.busy() {
		gc.Response(agentactor.Prepared{Err: failure})
		return
	}
	reply := gc.DeferResponse()
	if reply == nil {
		gc.Response(agentactor.Prepared{Err: errors.New("agentloop: close preparation requires an asynchronous request")})
		return
	}
	owner.awaitClosePreparation(gc, closePreparation{
		creationID: input.CreationID,
		reply:      reply,
	})
}

func (owner *Grain) awaitClosePreparation(gc *goakt.GrainContext, input closePreparation) {
	owner.refreshIdle()
	signal := owner.idle
	stopped := owner.stopped
	err := gc.PipeToSelf(func() (any, error) {
		select {
		case <-signal:
			return input, nil
		case <-stopped:
			input.reply.Response(agentactor.Prepared{Err: errors.New("agentloop: stopped while preparing close")})
			return nil, nil
		}
	})
	if err != nil {
		input.reply.Response(agentactor.Prepared{Err: err})
	}
}

func (owner *Grain) confirmClosePreparation(gc *goakt.GrainContext, input closePreparation) {
	failure := owner.checkCreation(input.creationID)
	if failure == nil && owner.busy() {
		owner.awaitClosePreparation(gc, input)
		return
	}
	if failure == nil {
		failure = owner.business.CheckClose(false)
	}
	input.reply.Response(agentactor.Prepared{Err: failure})
}
