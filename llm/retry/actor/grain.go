package retryactor

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"

	"github.com/google/uuid"
	"github.com/gorenx/goren/agent"
	middlewareactor "github.com/gorenx/goren/agent/middleware/actor"
	llmretry "github.com/gorenx/goren/llm/retry"
	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

// Initialize binds one Agent's recovery middleware to its Session request route.
type Initialize struct {
	AgentID    session.SessionID
	Session    *goakt.GrainIdentity
	CreationID string
}

// Completion acknowledges initialization; invocation replies use Agent hook contracts.
type Completion struct{ Err error }

// Grain schedules Recovery decisions without implementing retry policy or history rules.
type Grain struct {
	agentID      session.SessionID
	conversation *goakt.GrainIdentity
	creationID   string
	active       *invocation
	stopped      chan struct{}
}

type invocation struct {
	business  *llmretry.Recovery
	address   middlewareactor.Invocation
	reply     *goakt.GrainReply
	operation context.Context
	cancel    context.CancelCauseFunc
}
type advanced struct {
	call   *invocation
	result llmretry.Result
}

// OnActivate creates only a framework shutdown signal.
func (owner *Grain) OnActivate(context.Context, *goakt.GrainProps) error {
	owner.stopped = make(chan struct{})
	return nil
}

// OnDeactivate interrupts retained waits and fails any unfinished original invocation.
func (owner *Grain) OnDeactivate(context.Context, *goakt.GrainProps) error {
	close(owner.stopped)
	if owner.active != nil {
		owner.finish(owner.active, agent.RequestErrorResolved{
			Err: errors.New("llm-retry: middleware stopped during recovery"),
		})
	}
	return nil
}

// OnReceive handles framework requests and returns effect results to Recovery.
func (owner *Grain) OnReceive(gc *goakt.GrainContext) {
	switch input := gc.Message().(type) {
	case Initialize:
		if owner.conversation != nil || input.AgentID == "" || input.Session == nil || input.CreationID == "" {
			gc.Response(Completion{
				Err: errors.New("llm-retry: fresh owner and Session binding are required"),
			})
			return
		}
		owner.agentID = input.AgentID
		owner.conversation = input.Session
		owner.creationID = input.CreationID
		gc.Response(Completion{})
	case middlewareactor.InvokeRequestError:
		owner.start(gc, input)
	case middlewareactor.CancelInvocation:
		call := owner.active
		if call != nil && call.address.CallID == input.CallID && call.address.Handler == input.Handler {
			call.business.Cancel(input.Cause)
			call.cancel(input.Cause)
		}
		gc.Response(middlewareactor.Completion{})
	case advanced:
		if owner.active != input.call {
			return
		}
		next, err := input.call.business.Advance(input.result)
		if err != nil {
			owner.finish(input.call, agent.RequestErrorResolved{Err: err})
			return
		}
		owner.execute(gc, input.call, next)
	default:
		gc.Unhandled()
	}
}

func (owner *Grain) start(gc *goakt.GrainContext, input middlewareactor.InvokeRequestError) {
	if owner.active != nil || owner.conversation == nil || input.Input.AgentID != owner.agentID || input.Invocation.Coordinator == nil || input.Invocation.CallID == "" || input.Invocation.Handler == "" {
		gc.Response(agent.RequestErrorResolved{
			Err: errors.New("llm-retry: exclusive invocation and matching Agent binding are required"),
		})
		return
	}
	reply := gc.DeferResponse()
	if reply == nil {
		gc.Response(agent.RequestErrorResolved{Err: errors.New("llm-retry: asynchronous invocation required")})
		return
	}
	business, err := llmretry.NewRecovery(input.Input, rand.Float64(), llmretry.RetryID(uuid.NewString()))
	if err != nil {
		reply.Response(agent.RequestErrorResolved{Err: err})
		return
	}
	operation, cancel := context.WithCancelCause(context.WithoutCancel(gc.Context()))
	call := &invocation{
		business:  business,
		address:   input.Invocation,
		reply:     reply,
		operation: operation,
		cancel:    cancel,
	}
	owner.active = call
	next, err := business.Start()
	if err != nil {
		owner.finish(call, agent.RequestErrorResolved{Err: err})
		return
	}
	owner.execute(gc, call, next)
}

func (owner *Grain) execute(gc *goakt.GrainContext, call *invocation, operation llmretry.Operation) {
	switch effect := operation.(type) {
	case llmretry.Completed:
		owner.finish(call, agent.RequestErrorResolved{
			Action: effect.Action,
			Err:    effect.Err,
		})
	case llmretry.Wait:
		stopped := owner.stopped
		if err := gc.PipeToSelf(func() (any, error) {
			timer := time.NewTimer(time.Duration(effect.Milliseconds * float64(time.Millisecond)))
			defer timer.Stop()
			var result llmretry.Result = llmretry.Waited{}
			select {
			case <-timer.C:
			case <-call.operation.Done():
				result = llmretry.Failed{Err: context.Cause(call.operation)}
			case <-stopped:
				return nil, nil
			}
			return advanced{
				call:   call,
				result: result,
			}, nil
		}); err != nil {
			owner.finish(call, agent.RequestErrorResolved{Err: err})
		}
	case llmretry.ReadHistory:
		owner.request(gc, call, operation, owner.conversation, sessionactor.Read{
			CreationID: owner.creationID,
		})
	case llmretry.Commit:
		owner.request(gc, call, operation, owner.conversation, sessionactor.Commit{
			CreationID: owner.creationID,
			Plan:       effect.Plan,
		})
	case llmretry.Continue:
		owner.request(gc, call, operation, call.address.Coordinator, middlewareactor.ContinueRequestError{
			CallID:  call.address.CallID,
			Handler: call.address.Handler,
		})
	default:
		owner.finish(call, agent.RequestErrorResolved{Err: errors.New("llm-retry: unsupported recovery effect")})
	}
}

func (owner *Grain) request(gc *goakt.GrainContext, call *invocation, operation llmretry.Operation, target *goakt.GrainIdentity, message any) {
	completed := make(chan advanced, 1)
	stopped := owner.stopped
	if err := gc.PipeToSelf(func() (any, error) {
		select {
		case result := <-completed:
			return result, nil
		case <-stopped:
			return nil, nil
		}
	}); err != nil {
		owner.finish(call, agent.RequestErrorResolved{Err: err})
		return
	}
	gc.RequestGrain(target, message, goakt.WithRequestTimeout(0), goakt.WithReentrancyMode(reentrancy.AllowAll)).Then(func(payload any, failure error) {
		completed <- advanced{
			call:   call,
			result: response(operation, payload, failure),
		}
	})
}

func response(operation llmretry.Operation, payload any, failure error) llmretry.Result {
	if _, delegated := operation.(llmretry.Continue); delegated {
		result, ok := payload.(agent.RequestErrorResolved)
		if failure != nil {
			result.Err = failure
		} else if !ok {
			result.Err = errors.New("llm-retry: invalid downstream recovery response")
		}
		return llmretry.Continued{
			Action: result.Action,
			Err:    result.Err,
		}
	}
	if failure == nil {
		switch operation.(type) {
		case llmretry.ReadHistory:
			if result, ok := payload.(sessionactor.View); ok {
				if result.Err == nil {
					return llmretry.HistoryRead{Snapshot: result.Snapshot}
				}
				failure = result.Err
			}
		case llmretry.Commit:
			if result, ok := payload.(sessionactor.Committed); ok {
				if result.Err == nil {
					return llmretry.Committed{Write: result.Result}
				}
				failure = result.Err
			}
		}
	}
	if failure == nil {
		failure = errors.New("llm-retry: invalid Session response")
	}
	return llmretry.Failed{Err: failure}
}

func (owner *Grain) finish(call *invocation, result agent.RequestErrorResolved) {
	if owner.active != call {
		return
	}
	call.cancel(nil)
	call.reply.Response(result)
	owner.active = nil
}
