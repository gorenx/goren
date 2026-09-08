package loopactor

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/gorenx/goren/agentloop/internal/modelrequest"
	llmactor "github.com/gorenx/goren/llm/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
)

func (owner *Grain) sendModelOperation(gc *goakt.GrainContext, current *modelInvocation, operation modelrequest.Operation) {
	if _, generating := operation.(modelrequest.Generate); generating {
		current.callID = llmactor.CallID(uuid.NewString())
	}
	if current.callID == "" {
		if _, closing := operation.(modelrequest.CloseStream); closing {
			owner.acceptRequest(gc, current, modelrequest.AttemptAdvanced{
				Result: modelrequest.StreamClosed{},
			})
		} else {
			owner.finishModel(current, errors.New("agentloop: no active LLM call"))
		}
		return
	}
	current.pendingID = llmactor.RequestID(uuid.NewString())
	current.pendingOperation = operation
	route := llmactor.Delivery{
		CallID:    current.callID,
		RequestID: current.pendingID,
		ReplyTo:   gc.Self(),
	}
	var message any
	switch input := operation.(type) {
	case modelrequest.Generate:
		message = llmactor.Generation{
			Delivery: route,
			Settings: llmactor.AgentConfig{
				AgentID: string(owner.business.ID()),
			},
			Input: input.Input,
		}
	case modelrequest.ReadChunk:
		message = llmactor.ReadChunk{
			Delivery: route,
		}
	case modelrequest.CloseStream:
		message = llmactor.CloseCall{
			Delivery: route,
		}
	default:
		owner.finishModel(current, errors.New("agentloop: invalid LLM operation"))
		return
	}
	if err := owner.awaitGeneration(gc, current); err != nil {
		owner.advanceStream(gc, streamProgress{
			attempt:   current,
			operation: operation,
			err:       err,
		})
		return
	}
	if err := goakt.Tell(gc.Context(), current.input.router, goakt.NewBroadcast(message)); err != nil {
		owner.advanceStream(gc, streamProgress{
			attempt:   current,
			operation: operation,
			err:       err,
		})
	}
}

func (owner *Grain) generationResult(gc *goakt.GrainContext, id llmactor.CallID, requestID llmactor.RequestID, payload any) {
	// TellGrain waits for this delivery acknowledgement, not for the next
	// Session commit or provider read selected by the business state machine.
	gc.NoErr()
	current := owner.model
	if current == nil || current.callID != id || current.pendingID != requestID {
		return
	}
	operation := current.pendingOperation
	if operation == nil {
		return
	}
	current.pendingOperation = nil
	current.pendingID = ""
	if result, ok := payload.(llmactor.CallClosed); ok && result.Err == nil {
		current.callID = ""
	}
	owner.advanceStream(gc, streamProgress{
		attempt:   current,
		operation: operation,
		payload:   payload,
	})
}

// generationExpired identifies only the operation whose reply deadline elapsed.
// A late timer cannot expire another read or a later generation.
type generationExpired struct {
	callID    llmactor.CallID
	requestID llmactor.RequestID
}

func (owner *Grain) awaitGeneration(gc *goakt.GrainContext, current *modelInvocation) error {
	lifetime, cancel := context.WithCancel(context.WithoutCancel(gc.Context()))
	current.cancel = cancel
	timeout := current.input.replyTimeout
	expired := generationExpired{
		callID:    current.callID,
		requestID: current.pendingID,
	}
	stopped := owner.stopped
	return gc.PipeToSelf(func() (any, error) {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		select {
		case <-timer.C:
			return expired, nil
		case <-lifetime.Done():
			return nil, nil
		case <-stopped:
			return nil, nil
		}
	})
}

func (owner *Grain) expireGeneration(gc *goakt.GrainContext, input generationExpired) {
	gc.NoErr()
	current := owner.model
	if current == nil || current.callID != input.callID || current.pendingID != input.requestID {
		return
	}
	operation := current.pendingOperation
	if operation == nil {
		return
	}
	current.pendingID = ""
	current.pendingOperation = nil
	owner.advanceStream(gc, streamProgress{
		attempt:   current,
		operation: operation,
		err:       errors.New("agentloop: LLM operation result timed out"),
	})
}
