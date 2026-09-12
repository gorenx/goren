package toolactor

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/tools"
	goakt "github.com/tochemey/goakt/v4/actor"
)

type call struct {
	reply  *goakt.GrainReply
	cancel context.CancelFunc
}

type executed struct {
	identifier agentmessage.CallID
	call       *call
	result     tools.BodyResult
}

func (owner *Grain) receiveExecution(gc *goakt.GrainContext) bool {
	switch input := gc.Message().(type) {
	case Describe:
		if owner.business == nil || input.CreationID != owner.creationID || owner.active != nil {
			gc.Response(Description{Err: errors.New("tools: description does not own a settled tool")})
			return true
		}
		selected, descriptor, err := owner.business.Binding()
		if err != nil {
			gc.Response(Description{Err: err})
		} else {
			gc.Response(Description{
				Revision:   selected,
				Descriptor: descriptor,
			})
		}
	case Execute:
		owner.execute(gc, input)
	case CancelCall:
		if owner.business == nil || input.CreationID != owner.creationID {
			gc.Response(Completion{
				Err: errors.New("tools: cancellation does not own this tool creation"),
			})
			return true
		}
		if current := owner.calls[input.CallID]; current != nil {
			current.cancel()
		}
		gc.Response(Completion{})
	case executed:
		gc.NoErr()
		if owner.calls[input.identifier] != input.call {
			return true
		}
		delete(owner.calls, input.identifier)
		input.call.cancel()
		input.call.reply.Response(input.result)
	default:
		return false
	}
	return true
}

func (owner *Grain) execute(gc *goakt.GrainContext, input Execute) {
	if owner.business == nil ||
		input.CreationID != owner.creationID ||
		input.Invocation.SessionID != owner.agentID ||
		input.Invocation.CallID == "" ||
		owner.active != nil ||
		owner.calls[input.Invocation.CallID] != nil {
		gc.Response(tools.BodyResult{
			Err: errors.New("tools: execution identity is invalid or the tool is occupied"),
		})
		return
	}
	reply := gc.DeferResponse()
	if reply == nil {
		gc.Response(tools.BodyResult{
			Err: errors.New("tools: execution requires an asynchronous request"),
		})
		return
	}
	ctx := input.Context
	if ctx == nil {
		ctx = context.WithoutCancel(gc.Context())
	}
	work, cancel := context.WithCancel(tools.WithAgentCreationID(ctx, owner.agentCreationID))
	current := &call{
		reply:  reply,
		cancel: cancel,
	}
	if owner.calls == nil {
		owner.calls = make(map[agentmessage.CallID]*call)
	}
	owner.calls[input.Invocation.CallID] = current
	business := owner.business
	// Capture the caller's bytes before the framework task starts.
	input.Invocation.Arguments = input.Invocation.ArgumentsJSON()
	if err := gc.PipeToSelf(func() (any, error) {
		return executed{
			identifier: input.Invocation.CallID,
			call:       current,
			result:     business.Execute(work, input.Revision, input.Invocation),
		}, nil
	}); err != nil {
		delete(owner.calls, input.Invocation.CallID)
		cancel()
		reply.Response(tools.BodyResult{
			Err: err,
		})
	}
}
