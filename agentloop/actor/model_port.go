package loopactor

import (
	"errors"

	"github.com/gorenx/goren/agentloop"
	"github.com/gorenx/goren/llm"
	llmactor "github.com/gorenx/goren/llm/actor"
)

// modelPort adapts only LLM operations. The business receiver owns streaming,
// Session commits, recovery and all decisions about subsequent operations.
type modelPort struct{ scope serviceScope }

func (endpoint modelPort) Generate(input llm.Input, receiver agentloop.GenerationReceiver) error {
	owner := endpoint.scope.owner
	if owner.callID != "" || owner.generation != nil {
		return errors.New("agentloop: generation is already open")
	}
	if owner.models == nil {
		client, err := llmactor.NewClient(owner.modelRouter, owner.replyTimeout)
		if err != nil {
			return err
		}
		owner.models = client
	}
	owner.callID = owner.models.NewCall()
	owner.generation = receiver
	owner.models.Start(endpoint.scope.gc, owner.callID, llmactor.AgentConfig{
		AgentID: string(owner.business.ID()),
	}, input, owner)
	return nil
}

func (endpoint modelPort) Read(receiver agentloop.ChunkReceiver) error {
	owner := endpoint.scope.owner
	if owner.callID == "" || owner.chunk != nil {
		return errors.New("agentloop: chunk read requires an available generation")
	}
	owner.chunk = receiver
	owner.models.Read(endpoint.scope.gc, owner.callID, owner)
	return nil
}

func (endpoint modelPort) Close(receiver agentloop.StreamReceiver) error {
	owner := endpoint.scope.owner
	if owner.stream != nil {
		return errors.New("agentloop: stream close is already pending")
	}
	if owner.callID == "" {
		return endpoint.scope.deliver(func(services agentloop.Services) { receiver.StreamClosed(services, nil) })
	}
	owner.stream = receiver
	owner.models.Close(endpoint.scope.gc, owner.callID, owner)
	return nil
}
