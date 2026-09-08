package registry

import (
	"context"
	"errors"

	agentactor "github.com/gorenx/goren/agent/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

type closeStage uint8

const (
	prepare closeStage = iota
	children
	resources
	stop
)

// closure retains completed lifecycle steps across retries. It never owns an
// AgentLoop object; executor is only a framework address.
type closure struct {
	identity Creation
	executor *goakt.GrainIdentity
	children []Creation
	next     int
	stage    closeStage
	// replies are framework reply handles for callers sharing this close attempt.
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
	if entry.executor == nil {
		gc.Response(agentactor.Closed{
			Err: errors.New("agent registry: creation has no claimed executor; initialization cleanup is required"),
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
			Err: errors.New("agent registry: close requires an asynchronous request"),
		})
		return
	}
	operation := entry.closure
	if operation == nil {
		operation = &closure{
			identity: input.Creation,
			executor: entry.executor,
			children: accepted.Children,
		}
		entry.closure = operation
		owner.creations[input.Creation.AgentID] = entry
	}
	operation.replies = append(operation.replies, reply)
	if operation.cancel != nil {
		return
	}
	owner.runClose(gc, operation)
}

func (owner *Grain) runClose(gc *goakt.GrainContext, operation *closure) {
	var target *goakt.GrainIdentity
	var message any
	switch operation.stage {
	case prepare:
		target = operation.executor
		message = agentactor.PrepareClose{
			CreationID: operation.identity.CreationID,
		}
	case children:
		// A child may have completed an independent close while this parent was
		// waiting or retrying. Its replacement is not part of this closing tree.
		for operation.next < len(operation.children) {
			child := operation.children[operation.next]
			entry, found := owner.creations[child.AgentID]
			if found && !entry.closed && entry.creationID == child.CreationID {
				break
			}
			operation.next++
		}
		if operation.next == len(operation.children) {
			operation.stage = resources
			owner.runClose(gc, operation)
			return
		}
		target = gc.Self()
		message = CloseAgent{
			Creation: operation.children[operation.next],
		}
	case resources:
		target = operation.executor
		message = agentactor.CloseResources{
			CreationID: operation.identity.CreationID,
		}
	}
	ctx, cancel := context.WithCancel(context.WithoutCancel(gc.Context()))
	operation.cancel = cancel
	if operation.stage == stop {
		system := gc.ActorSystem()
		identity := operation.executor
		if err := gc.PipeToSelf(func() (any, error) {
			return closeAdvanced{
				operation: operation,
				err:       system.TellGrain(ctx, identity, new(goakt.PoisonPill)),
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
	if failure == nil {
		switch operation.stage {
		case prepare:
			result, ok := input.payload.(agentactor.Prepared)
			if !ok {
				failure = errors.New("agent registry: invalid prepare-close response")
			} else {
				failure = result.Err
			}
		case children, resources:
			result, ok := input.payload.(agentactor.Closed)
			if !ok {
				failure = errors.New("agent registry: invalid close response")
			} else {
				failure = result.Err
			}
		}
	}
	if failure != nil {
		operation.respond(failure)
		return
	}
	switch operation.stage {
	case prepare:
		operation.stage = children
	case children:
		operation.next++
	case resources:
		operation.stage = stop
	case stop:
		if err := owner.directory.Remove(operation.identity.AgentID); err != nil {
			operation.respond(err)
			return
		}
		entry.closed = true
		entry.closure = nil
		entry.executor = nil
		owner.creations[operation.identity.AgentID] = entry
		operation.respond(nil)
		return
	}
	owner.runClose(gc, operation)
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
