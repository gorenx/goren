package registry

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agent"
	agentactor "github.com/gorenx/goren/agent/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// closure retains only request correlation and the current framework stop.
// The business resource ledger selects dependencies and retains failed cleanup.
type closure struct {
	identity Creation
	resource agent.Resource
	// replies are deferred requests sharing this close attempt, not a work queue.
	replies []*goakt.GrainReply
	cancel  context.CancelFunc
}

// shutdownResult carries one shutdown operation's reply or failure. Only a
// confirmed result permits the next resource to close; failure retains the step.
type shutdownResult struct {
	operation *closure
	err       error
}

func (owner *Grain) closeAgent(gc *goakt.GrainContext, input CloseAgent) {
	entry, found := owner.creations[input.Creation.AgentID]
	if found && owner.directory.IsClosed(input.Creation.AgentID) && entry.creationID == input.Creation.CreationID {
		gc.Response(agentactor.Closed{})
		return
	}
	reply, err := deferLifecycle(gc)
	if err != nil {
		gc.Response(agentactor.Closed{Err: err})
		return
	}
	owner.scheduleClose(gc, input.Creation, reply)
}

// scheduleClose shares explicit close requests and automatic startup cleanup.
// The business directory has already frozen admission before any cleanup starts.
func (owner *Grain) scheduleClose(gc *goakt.GrainContext, identity Creation, reply *goakt.GrainReply) {
	accepted := owner.beginClose(identity)
	if accepted.Err != nil {
		if reply != nil {
			reply.Response(agentactor.Closed{Err: accepted.Err})
		}
		return
	}
	entry := owner.creations[identity.AgentID]
	operation := entry.closure
	if operation == nil {
		operation = &closure{identity: identity}
		entry.closure = operation
		owner.creations[identity.AgentID] = entry
	}
	if reply != nil {
		operation.replies = append(operation.replies, reply)
	}
	if operation.cancel == nil {
		owner.runClose(gc, operation)
	}
}

func (owner *Grain) runClose(gc *goakt.GrainContext, operation *closure) {
	entry := owner.creations[operation.identity.AgentID]
	if entry.activation != nil && entry.activation.cancel != nil {
		return
	}
	resource, pending, err := owner.directory.NextRelease(operation.identity.AgentID)
	if err != nil {
		operation.respond(err)
		return
	}
	if !pending {
		owner.finishClose(operation)
		return
	}
	endpoint := entry.releases[resource]
	if endpoint == nil {
		operation.respond(errors.New("agent registry: resource client is missing"))
		return
	}
	operation.resource = resource
	ctx, cancel := context.WithCancel(context.WithoutCancel(gc.Context()))
	operation.cancel = cancel
	completed := make(chan error, 1)
	err = gc.PipeToSelf(func() (any, error) {
		select {
		case failure := <-completed:
			return shutdownResult{
				operation: operation,
				err:       failure,
			}, nil
		case <-ctx.Done():
			return shutdownResult{
				operation: operation,
				err:       context.Cause(ctx),
			}, nil
		}
	})
	if err != nil {
		operation.respond(err)
		return
	}
	endpoint.Close(gc, completed)
}

func (owner *Grain) handleShutdownResult(gc *goakt.GrainContext, input shutdownResult) {
	operation := input.operation
	entry, found := owner.creations[operation.identity.AgentID]
	if !found || entry.creationID != operation.identity.CreationID ||
		entry.closure != operation || operation.cancel == nil {
		return
	}
	operation.cancel()
	operation.cancel = nil
	if input.err != nil {
		operation.respond(input.err)
		return
	}
	err := owner.directory.ConfirmRelease(operation.identity.AgentID, operation.resource)
	if err != nil {
		operation.respond(err)
		return
	}
	delete(entry.releases, operation.resource)
	owner.runClose(gc, operation)
}

func (owner *Grain) finishClose(operation *closure) {
	err := owner.directory.Remove(operation.identity.AgentID)
	if err != nil {
		operation.respond(err)
		return
	}
	entry := owner.creations[operation.identity.AgentID]
	entry.closure = nil
	entry.activation = nil
	entry.services = nil
	entry.executor = nil
	owner.creations[operation.identity.AgentID] = entry
	operation.respond(nil)
}

func (operation *closure) respond(failure error) {
	if operation.cancel != nil {
		operation.cancel()
		operation.cancel = nil
	}
	for _, reply := range operation.replies {
		reply.Response(agentactor.Closed{Err: failure})
	}
	operation.replies = nil
}
