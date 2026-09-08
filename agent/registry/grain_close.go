package registry

import (
	"context"
	"errors"

	agentactor "github.com/gorenx/goren/agent/actor"
	commandactor "github.com/gorenx/goren/commands/actor"
	sessionactor "github.com/gorenx/goren/session/actor"
	sessionregistry "github.com/gorenx/goren/session/registry"
	promptactor "github.com/gorenx/goren/systemprompt/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

type closeStage uint8

const (
	prepare closeStage = iota
	children
	closeCommands
	closeComponents
	resources
	closePrompt
	closeSession
	stop
)

// closure retains confirmed steps across retries, without owning business objects.
type closure struct {
	componentNext int
	identity      Creation
	children      []Creation
	next          int
	stage         closeStage
	// deactivating means this component confirmed business close. A framework
	// stop retry must not send its business close again to a fresh activation.
	deactivating bool
	// replies are deferred requests sharing this close attempt, not a work queue.
	replies []*goakt.GrainReply
	cancel  context.CancelFunc
}

type closeAdvanced struct {
	operation *closure
	payload   any
	err       error
}

func (owner *Grain) closeAgent(gc *goakt.GrainContext, input CloseAgent) {
	entry, found := owner.creations[input.Creation.AgentID]
	if found && entry.closed && entry.creationID == input.Creation.CreationID {
		gc.Response(agentactor.Closed{})
		return
	}
	accepted := owner.beginClose(input.Creation)
	if accepted.Err != nil {
		gc.Response(agentactor.Closed{
			Err: accepted.Err,
		})
		return
	}
	if err := gc.EnableReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))); err != nil {
		gc.Response(agentactor.Closed{
			Err: err,
		})
		return
	}
	reply := gc.DeferResponse()
	if reply == nil {
		gc.Response(agentactor.Closed{
			Err: errors.New("agent registry: close requires a request"),
		})
		return
	}
	operation := entry.closure
	if operation == nil {
		operation = &closure{
			identity: input.Creation,
			children: accepted.Children,
		}
		entry.closure = operation
		owner.creations[input.Creation.AgentID] = entry
	}
	operation.replies = append(operation.replies, reply)
	if operation.cancel == nil {
		owner.runClose(gc, operation)
	}
}

func (owner *Grain) runClose(gc *goakt.GrainContext, operation *closure) {
	entry := owner.creations[operation.identity.AgentID]
	components := entry.activation
	if components != nil && components.initializing {
		return
	}
	var target *goakt.GrainIdentity
	var message any
	switch operation.stage {
	case prepare:
		if entry.instanceID != "" {
			target = entry.executor
			message = agentactor.PrepareClose{
				CreationID: operation.identity.CreationID,
			}
		}
	case children:
		for operation.next < len(operation.children) {
			descendant := operation.children[operation.next]
			current, found := owner.creations[descendant.AgentID]
			if found && !current.closed && current.creationID == descendant.CreationID {
				break
			}
			operation.next++
		}
		if operation.next < len(operation.children) {
			target = gc.Self()
			message = CloseAgent{
				Creation: operation.children[operation.next],
			}
		}
	case closeCommands:
		if components != nil {
			target = components.commands
			message = commandactor.Close{
				CreationID: operation.identity.CreationID,
			}
		}
	case closeComponents:
		if components != nil && operation.componentNext < len(components.components) {
			position := len(components.components) - 1 - operation.componentNext
			target = components.components[position].Owner
			message = agentactor.StopComponent{
				AgentID:    operation.identity.AgentID,
				CreationID: operation.identity.CreationID,
			}
		}
	case resources:
		if entry.instanceID != "" {
			target = entry.executor
			message = agentactor.CloseResources{
				CreationID: operation.identity.CreationID,
			}
		}
	case closePrompt:
		if components != nil {
			target = components.prompt
			message = promptactor.CloseRuntime{}
		}
	case closeSession:
		if components != nil && components.sessionRegistered {
			target = components.input.SessionRegistry
			message = sessionregistry.CloseSession{
				SessionID:  operation.identity.AgentID,
				CreationID: operation.identity.CreationID,
			}
		}
	case stop:
		target = entry.executor
	}
	if target == nil {
		owner.nextClose(gc, operation)
		return
	}
	ctx, cancel := context.WithCancel(context.WithoutCancel(gc.Context()))
	operation.cancel = cancel
	if operation.deactivating || operation.stage == stop {
		system := gc.ActorSystem()
		if err := gc.PipeToSelf(func() (any, error) {
			return closeAdvanced{
				operation: operation,
				err:       system.TellGrain(ctx, target, new(goakt.PoisonPill)),
			}, nil
		}); err != nil {
			operation.respond(err)
		}
		return
	}
	completed := make(chan closeAdvanced, 1)
	if err := gc.PipeToSelf(func() (any, error) {
		select {
		case result := <-completed:
			return result, nil
		case <-ctx.Done():
			return closeAdvanced{
				operation: operation,
				err:       context.Cause(ctx),
			}, nil
		}
	}); err != nil {
		operation.respond(err)
		return
	}
	gc.RequestGrain(target, message, goakt.WithRequestTimeout(0)).Then(func(payload any, failure error) {
		completed <- closeAdvanced{
			operation: operation,
			payload:   payload,
			err:       failure,
		}
	})
}

func (owner *Grain) advanceClose(gc *goakt.GrainContext, input closeAdvanced) {
	operation := input.operation
	entry, found := owner.creations[operation.identity.AgentID]
	if !found || entry.creationID != operation.identity.CreationID || entry.closure != operation {
		return
	}
	operation.cancel()
	operation.cancel = nil
	failure := input.err
	if failure == nil && !operation.deactivating {
		switch operation.stage {
		case prepare:
			result, ok := input.payload.(agentactor.Prepared)
			if !ok {
				failure = errors.New("agent registry: invalid preparation reply")
			} else {
				failure = result.Err
			}
		case children, resources:
			result, ok := input.payload.(agentactor.Closed)
			if !ok {
				failure = errors.New("agent registry: invalid Agent close reply")
			} else {
				failure = result.Err
			}
		case closeCommands:
			result, ok := input.payload.(commandactor.Completion)
			if !ok {
				failure = errors.New("agent registry: invalid Commands close reply")
			} else {
				failure = result.Err
			}
		case closeComponents:
			result, ok := input.payload.(agentactor.ComponentStopped)
			if !ok {
				failure = errors.New("agent registry: invalid component close reply")
			} else {
				failure = result.Err
			}
		case closePrompt:
			result, ok := input.payload.(promptactor.Completion)
			if !ok {
				failure = errors.New("agent registry: invalid Prompt close reply")
			} else {
				failure = result.Err
			}
		case closeSession:
			result, ok := input.payload.(sessionactor.Closed)
			if !ok {
				failure = errors.New("agent registry: invalid Session close reply")
			} else {
				failure = result.Err
			}
		}
	}
	if failure != nil {
		operation.respond(failure)
		return
	}
	if !operation.deactivating && (operation.stage == closeCommands || operation.stage == closePrompt) {
		operation.deactivating = true
		owner.runClose(gc, operation)
		return
	}
	operation.deactivating = false
	if operation.stage == closeComponents {
		operation.componentNext++
		owner.runClose(gc, operation)
		return
	}
	if operation.stage == children {
		operation.next++
		owner.runClose(gc, operation)
		return
	}
	owner.nextClose(gc, operation)
}

func (owner *Grain) nextClose(gc *goakt.GrainContext, operation *closure) {
	if operation.stage != stop {
		operation.stage++
		owner.runClose(gc, operation)
		return
	}
	if err := owner.directory.Remove(operation.identity.AgentID); err != nil {
		operation.respond(err)
		return
	}
	entry := owner.creations[operation.identity.AgentID]
	entry.closed = true
	entry.closure = nil
	entry.activation = nil
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
		reply.Response(agentactor.Closed{
			Err: failure,
		})
	}
	operation.replies = nil
}
