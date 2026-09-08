package loopactor

import (
	"errors"

	"github.com/gorenx/goren/agentloop/internal/modelrequest"
	llmactor "github.com/gorenx/goren/llm/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
)

func (owner *Grain) sendModelOperation(gc *goakt.GrainContext, current *modelInvocation, operation modelrequest.Operation) {
	if _, generating := operation.(modelrequest.Generate); generating {
		current.callID = current.models.NewCall()
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
	current.pendingOperation = operation
	switch input := operation.(type) {
	case modelrequest.Generate:
		current.models.Start(gc, current.callID, llmactor.AgentConfig{
			AgentID: string(owner.business.ID()),
		}, input.Input, owner)
	case modelrequest.ReadChunk:
		current.models.Read(gc, current.callID, owner)
	case modelrequest.CloseStream:
		current.models.Close(gc, current.callID, owner)
	default:
		owner.finishModel(current, errors.New("agentloop: invalid LLM operation"))
	}
}

// OnGenerationStarted delivers resolved parameters and opening outcome to ModelRequest.
func (owner *Grain) OnGenerationStarted(gc *goakt.GrainContext, result llmactor.GenerationStarted) {
	owner.generationResult(gc, result.CallID, result)
}

// OnChunkRead delivers one requested chunk without deciding request policy.
func (owner *Grain) OnChunkRead(gc *goakt.GrainContext, result llmactor.ChunkRead) {
	owner.generationResult(gc, result.CallID, result)
}

// OnCallClosed delivers the actual close outcome to ModelRequest.
func (owner *Grain) OnCallClosed(gc *goakt.GrainContext, result llmactor.CallClosed) {
	owner.generationResult(gc, result.CallID, result)
}

func (owner *Grain) generationResult(gc *goakt.GrainContext, id llmactor.CallID, payload any) {
	current := owner.model
	if current == nil || current.callID != id {
		return
	}
	operation := current.pendingOperation
	if operation == nil {
		return
	}
	current.pendingOperation = nil
	if result, ok := payload.(llmactor.CallClosed); ok && result.Err == nil {
		current.callID = ""
	}
	owner.advanceStream(gc, streamProgress{
		attempt:   current,
		operation: operation,
		payload:   payload,
	})
}
