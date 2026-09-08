package loopactor

import (
	"errors"

	"github.com/gorenx/goren/agent"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// idleObserved belongs to a deferred runtime request, never the business Agent.
type idleObserved struct {
	creationID string
	reply      *goakt.GrainReply
}

func (owner *Grain) busy() bool {
	return owner.preparation != nil || owner.initialization != nil || owner.model != nil || owner.ending != nil ||
		owner.promptClosing != nil || owner.business.StatusValue() == agent.StatusRunning
}

func (owner *Grain) refreshIdle() {
	if owner.busy() {
		if owner.idle == nil {
			owner.idle = make(chan struct{})
		}
	} else if owner.idle != nil {
		close(owner.idle)
		owner.idle = nil
	}
}

func (owner *Grain) whenIdle(gc *goakt.GrainContext, input WhenIdle) {
	if err := owner.checkCreation(input.CreationID); err != nil {
		gc.Response(Completion{
			Err: err,
		})
		return
	}
	if !owner.busy() {
		gc.Response(Completion{})
		return
	}
	reply := gc.DeferResponse()
	if reply == nil {
		gc.Response(Completion{
			Err: errors.New("agentloop: waiting for idle requires an asynchronous request"),
		})
		return
	}
	owner.awaitIdle(gc, idleObserved{
		creationID: input.CreationID,
		reply:      reply,
	})
}

func (owner *Grain) awaitIdle(gc *goakt.GrainContext, input idleObserved) {
	owner.refreshIdle()
	signal := owner.idle
	stopped := owner.stopped
	if err := gc.PipeToSelf(func() (any, error) {
		select {
		case <-signal:
			return input, nil
		case <-stopped:
			// A stopped Grain cannot consume another completion. Fail the detached reply.
			input.reply.Response(Completion{
				Err: errors.New("agentloop: stopped while waiting for idle"),
			})
			return nil, nil
		}
	}); err != nil {
		input.reply.Response(Completion{
			Err: err,
		})
	}
}

func (owner *Grain) confirmIdle(gc *goakt.GrainContext, input idleObserved) {
	if err := owner.checkCreation(input.creationID); err != nil {
		input.reply.Response(Completion{
			Err: err,
		})
		return
	}
	// A later input may have reserved execution before this completion arrived.
	if owner.busy() {
		owner.awaitIdle(gc, input)
		return
	}
	input.reply.Response(Completion{})
}
