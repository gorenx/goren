package loopactor

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/gorenx/goren/agent"
	middlewareactor "github.com/gorenx/goren/agent/middleware/actor"
	"github.com/gorenx/goren/agentloop"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

// continueTurn asks what the current Turn should do after a Step has closed
// or preparation has finished without opening a Step. All preceding I/O must
// have completed before this request. It does not start a new Turn.
//
// The handler asks ReactLoopAgent for the business decision and awaits stopping
// hooks when required. The caller receives ContinueSteps or TurnEnd and must
// execute that next action; this request neither starts the next Step nor commits
// turn/end. The complete production driver has not yet been wired to this entry.
type continueTurn struct {
	middleware *goakt.GrainIdentity
	routes     []middlewareactor.Route
	// failure preserves the preceding execution error after Step cleanup.
	// Accepted cancellation is read from ReactLoopAgent, not supplied separately.
	failure error
}

// turnContinued reports the next business action, not a finished Turn.
// err means this boundary could not produce a valid action. A hook failure or
// accepted cancellation normally produces TurnEnd carrying that conclusion;
// it must still be committed and flushed by the caller.
type turnContinued struct {
	next agentloop.TurnOperation
	err  error
}

// stoppingInvocation owns transport resources, not the Turn's stopping state.
type stoppingInvocation struct {
	reply      *goakt.GrainReply
	id         middlewareactor.CallID
	middleware *goakt.GrainIdentity
	cancel     context.CancelFunc
}

type stoppingCompleted struct {
	operation *stoppingInvocation
	result    agent.TurnStoppingCompleted
}

// continueTurn schedules only the stopping hook selected by the business owner.
// The deferred reply waits for that hook, allowing Steer and Cancel meanwhile;
// CompleteStopping then rechecks committed input before selecting the next action.
func (owner *Grain) continueTurn(gc *goakt.GrainContext, input continueTurn) {
	if owner.stopping != nil || owner.preparation != nil ||
		owner.model != nil || owner.ending != nil || input.middleware == nil {
		gc.Response(turnContinued{
			err: errors.New("agentloop: Turn continuation requires released transport and middleware route"),
		})
		return
	}
	reply := gc.DeferResponse()
	if reply == nil {
		gc.Response(turnContinued{
			err: errors.New("agentloop: Turn continuation requires an asynchronous request"),
		})
		return
	}
	next, err := owner.business.ContinueTurn(input.failure)
	invocation, required := next.(agentloop.RunTurnStopping)
	if err != nil || !required {
		reply.Response(turnContinued{
			next: next,
			err:  err,
		})
		return
	}
	ctx, cancel := context.WithCancel(context.WithoutCancel(gc.Context()))
	operation := &stoppingInvocation{
		reply:      reply,
		id:         middlewareactor.CallID(uuid.NewString()),
		middleware: input.middleware,
		cancel:     cancel,
	}
	owner.stopping = operation
	completed := make(chan agent.TurnStoppingCompleted, 1)
	if err := gc.PipeToSelf(func() (any, error) {
		var result agent.TurnStoppingCompleted
		select {
		case result = <-completed:
		case <-ctx.Done():
			result.Err = context.Cause(ctx)
		}
		return stoppingCompleted{
			operation: operation,
			result:    result,
		}, nil
	}); err != nil {
		owner.completeStopping(stoppingCompleted{
			operation: operation,
			result: agent.TurnStoppingCompleted{
				Err: err,
			},
		})
		return
	}
	gc.RequestGrain(input.middleware, middlewareactor.RunTurnStopping{
		CallID: operation.id,
		Routes: append([]middlewareactor.Route(nil), input.routes...),
		Input:  invocation.Input,
	}, goakt.WithRequestTimeout(0), goakt.WithReentrancyMode(reentrancy.AllowAll)).Then(func(payload any, failure error) {
		result, ok := payload.(agent.TurnStoppingCompleted)
		if failure != nil {
			result.Err = failure
		} else if !ok {
			result.Err = errors.New("agentloop: unexpected stopping hook response")
		}
		completed <- result
	})
}

func (owner *Grain) completeStopping(input stoppingCompleted) {
	if owner.stopping != input.operation {
		return
	}
	next, err := owner.business.CompleteStopping(input.result)
	owner.finishStopping(input.operation, turnContinued{
		next: next,
		err:  err,
	})
}

func (owner *Grain) finishStopping(operation *stoppingInvocation, result turnContinued) {
	if owner.stopping != operation {
		return
	}
	operation.cancel()
	operation.reply.Response(result)
	owner.stopping = nil
}
