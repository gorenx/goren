package commandactor

import (
	"errors"
	"github.com/gorenx/goren/commands"
	sessionactor "github.com/gorenx/goren/session/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"time"
)

func (owner *Grain) receiveReply(gc *goakt.GrainContext, input replyReceived) {
	pending := input.request
	current := pending.call
	if owner.calls[current.input.CommandID] != current {
		return
	}
	switch pending.kind {
	case registryRequest:
		result, ok := input.payload.(Resolved)
		if input.err != nil || !ok || current.cause != nil {
			owner.failDelivery(current, errors.Join(input.err, current.cause, errors.New("commands: resolution failed")))
			owner.retire(current)
			return
		}
		if !result.Found {
			current.reply.Response(Executed{})
			current.reply = nil
			owner.retire(current)
			return
		}
		subject, err := commands.NewCommand(commands.Invocation{
			CommandID: current.input.CommandID,
			SessionID: owner.sessionID,
			RawInput:  current.parsed.RawInput,
		}, result.Registration.Definition, current.input.Options)
		if err != nil {
			owner.failDelivery(current, err)
			owner.retire(current)
			return
		}
		current.business = subject
		current.handler = result.Handler
		plan, err := subject.RunWrite()
		if err != nil {
			owner.failDelivery(current, err)
			owner.retire(current)
			return
		}
		owner.write(gc, current, plan, runRequest)
	case runRequest, doneRequest:
		owner.receiveCommit(gc, input)
	case handlerRequest:
		result, ok := input.payload.(Completed)
		if input.err != nil || !ok {
			owner.failDelivery(current, errors.Join(input.err, errors.New("commands: handler completion is unconfirmed")))
			return
		}
		owner.completeHandler(gc, current, result.Result, result.Err)
	case historyRequest:
		if current.write != nil || current.reply == nil {
			owner.retire(current)
			return
		}
		result, ok := input.payload.(sessionactor.View)
		failure := input.err
		if failure == nil && !ok {
			failure = errors.New("commands: invalid result history reply")
		}
		if failure == nil {
			failure = result.Err
		}
		if failure == nil {
			failure = current.business.Resolve(pending.result, result.Snapshot)
		}
		if failure != nil {
			_ = current.business.Fail(failure)
		}
		owner.writeDone(gc, current)
	}
}

func (owner *Grain) receiveCommit(gc *goakt.GrainContext, input replyReceived) {
	pending := input.request
	current := pending.call
	if current.write != pending {
		return
	}
	written, ok := input.payload.(sessionactor.Committed)
	if input.err != nil || !ok {
		if _, err := gc.ScheduleOnce(retryRequest{
			request: pending,
		}, 100*time.Millisecond); err != nil {
			owner.failDelivery(current, err)
		}
		return
	}
	if written.Err != nil {
		owner.failDelivery(current, written.Err)
		if pending.kind == runRequest {
			current.write = nil
			owner.retire(current)
		}
		return
	}
	var failure error
	if pending.kind == runRequest {
		failure = current.business.ConfirmRun(written.Result)
	} else {
		failure = current.business.ConfirmDone(written.Result)
	}
	if failure != nil {
		owner.failDelivery(current, failure)
		return
	}
	current.write = nil
	if pending.kind == doneRequest {
		result, failure := current.business.Result()
		reply := Executed{
			Err: failure,
		}
		if failure == nil {
			reply.Execution = &result
		}
		current.reply.Response(reply)
		current.reply = nil
		owner.retire(current)
		return
	}
	inputValue, err := current.business.Invocation()
	if err != nil {
		owner.writeDone(gc, current)
		return
	}
	current.handlerPending = true
	owner.send(gc, &request{
		call:   current,
		target: current.handler,
		message: Invoke{
			CreationID: owner.binding.CreationID,
			Input:      inputValue,
		},
		kind: handlerRequest,
	})
}

// completeHandler consumes the final reply associated with the original request.
func (owner *Grain) completeHandler(gc *goakt.GrainContext, current *invocation, result commands.Result, failure error) {
	current.handlerPending = false

	if current.reply == nil {
		owner.retire(current)
		return
	}
	if current.write != nil {
		return
	}
	if _, err := current.business.Invocation(); err != nil {
		owner.writeDone(gc, current)
		return
	}
	if failure != nil {
		if current.business.Fail(failure) == nil {
			owner.writeDone(gc, current)
		}
		return
	}
	owner.send(gc, &request{
		call:   current,
		target: owner.binding.Session,
		message: sessionactor.Read{
			CreationID: owner.binding.CreationID,
		},
		kind:   historyRequest,
		result: result,
	})

}
