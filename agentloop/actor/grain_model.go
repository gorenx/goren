package loopactor

import (
	"github.com/gorenx/goren/agentloop"
	"github.com/gorenx/goren/agentloop/internal/modelrequest"
	llmactor "github.com/gorenx/goren/llm/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
)

func (owner *Grain) OnGenerationStarted(gc *goakt.GrainContext, result llmactor.GenerationStarted) {
	receiver := owner.generation
	if receiver == nil || result.CallID != owner.callID {
		return
	}
	owner.generation = nil
	scope := serviceScope{
		owner: owner,
		gc:    gc,
	}
	err := scope.deliver(func(services agentloop.Services) {
		receiver.GenerationStarted(services, modelrequest.GenerationStarted{
			Config:      result.Config,
			Defaults:    result.Defaults,
			Context:     result.Context,
			RetryPolicy: result.RetryPolicy,
			Err:         result.Err,
		})
	})
	if err != nil {
		scope.ExecutionFailed(err)
	}
}

func (owner *Grain) OnChunkRead(gc *goakt.GrainContext, result llmactor.ChunkRead) {
	receiver := owner.chunk
	if receiver == nil || result.CallID != owner.callID {
		return
	}
	owner.chunk = nil
	scope := serviceScope{
		owner: owner,
		gc:    gc,
	}
	err := scope.deliver(func(services agentloop.Services) {
		receiver.ChunkRead(services, modelrequest.ChunkRead{
			Chunk: result.Chunk,
			Found: result.Found,
		}, result.Err)
	})
	if err != nil {
		scope.ExecutionFailed(err)
	}
}

func (owner *Grain) OnCallClosed(gc *goakt.GrainContext, result llmactor.CallClosed) {
	receiver := owner.stream
	if receiver == nil || result.CallID != owner.callID {
		return
	}
	owner.stream = nil
	if result.Err == nil {
		owner.callID = ""
	}
	scope := serviceScope{
		owner: owner,
		gc:    gc,
	}
	err := scope.deliver(func(services agentloop.Services) { receiver.StreamClosed(services, result.Err) })
	if err != nil {
		scope.ExecutionFailed(err)
	}
}
