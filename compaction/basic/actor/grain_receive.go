package compactionactor

import (
	"errors"
	agentactor "github.com/gorenx/goren/agent/actor"
	middlewareactor "github.com/gorenx/goren/agent/middleware/actor"
	"github.com/gorenx/goren/commands"
	"github.com/gorenx/goren/compaction"
	"github.com/gorenx/goren/compaction/basic"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Invocation messages admit work or deliver cancellation; only settled results release the original request.
func (owner *Grain) receiveInvocation(gc *goakt.GrainContext) bool {
	switch input := gc.Message().(type) {
	case agentactor.CancelMaintenance:
		owner.cancelMaintenance(gc, input)
	case Cancel:
		owner.cancel(gc, input)
	case middlewareactor.CancelInvocation:
		owner.cancelHook(gc, input)
	case middlewareactor.InvokePreStep:
		owner.startPressure(gc, input)
	case middlewareactor.InvokeRequestError:
		owner.startRecovery(gc, input)
	case Execute:
		owner.execute(gc, input, gc.DeferResponse(), "")
	default:
		return false
	}
	return true
}

// Binding messages install dependency routes or release an idle component.
func (owner *Grain) receiveBinding(gc *goakt.GrainContext) bool {
	switch input := gc.Message().(type) {
	case Initialize:
		gc.Response(Completion{
			Err: owner.initialize(gc, input),
		})
	case Close:
		if owner.active != nil {
			gc.Response(Completion{
				Err: errors.New("compaction: invocation must settle before close"),
			})
			return true
		}
		if owner.command != nil {
			owner.closeCommand(gc)
			return true
		}
		owner.closeResources(gc)
		gc.Response(Completion{})
	default:
		return false
	}
	return true
}

func (owner *Grain) execute(gc *goakt.GrainContext, input Execute, reply invocationReply, id commands.ID) {
	if owner.active != nil && input.Mode == basic.Manual {
		reply.Response(Result{
			Err: &compaction.ManualError{
				Code: compaction.ManualErrorBusy,
			},
		})
		return
	}
	if owner.business == nil ||
		owner.commandClosing() ||
		owner.active != nil ||
		input.RequestID == "" ||
		(input.Mode == basic.Manual && input.Agent == nil) {
		reply.Response(Result{
			Err: errors.New("compaction: initialized idle binding and request identity are required"),
		})
		return
	}
	current := &invocation{
		reply:     reply,
		commandID: id,
		input:     input,
	}
	if input.SourceCommandID != nil {
		detached := *input.SourceCommandID
		current.input.SourceCommandID = &detached
	}
	owner.active = current
	if input.Mode == basic.Manual {
		owner.beginMaintenance(gc, current)
	} else {
		owner.prepare(gc, current)
	}
}

func (owner *Grain) closeResources(gc *goakt.GrainContext) {
	if owner.models != nil {
		owner.models.Dispose(gc)
	}
	owner.executor = nil
	owner.business = nil
	owner.pruner = nil
}

// commandClosing prevents new work while asynchronous registry withdrawal runs.
func (owner *Grain) commandClosing() bool {
	return owner.command != nil && owner.command.phase == registrationClosing
}
