package loopactor

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/gorenx/goren/llm"

	"github.com/google/uuid"
	"github.com/gorenx/goren/agent"
	middlewareactor "github.com/gorenx/goren/agent/middleware/actor"
	"github.com/gorenx/goren/agentloop/internal/modelrequest"
	"github.com/gorenx/goren/agentmessage"
	llmactor "github.com/gorenx/goren/llm/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

// requestModel supplies transport routes; the business Agent owns all request input.
type requestModel struct {
	router         *goakt.PID
	middleware     *goakt.GrainIdentity
	recoveryRoutes []middlewareactor.Route
	// replyTimeout bounds each LLM result wait, independently of provider I/O timeouts.
	replyTimeout time.Duration
}

type modelResult struct {
	message agentmessage.AssistantMessage
	finish  llm.FinishReason
	err     error
}

// modelInvocation retains transport resources for the Agent-owned ModelRequest.
// It never creates an Attempt or chooses a business retry policy.
type modelInvocation struct {
	input            requestModel
	callID           llmactor.CallID
	models           *llmactor.Client
	pendingOperation modelrequest.Operation
	canceling        *modelCancellation
	reply            *goakt.GrainReply
	cancel           context.CancelFunc
	cleanup          *goakt.GrainReply
	cancellation     *cancellationRequest
	outcome          modelrequest.Completed
}

type requestProgress struct {
	invocation *modelInvocation
	operation  modelrequest.RequestOperation
	payload    any
	err        error
}

func (owner *Grain) startModel(gc *goakt.GrainContext, input requestModel) {
	if owner.model != nil || owner.preparation != nil || input.router == nil || input.middleware == nil || input.replyTimeout <= 0 {
		gc.Response(modelResult{
			err: errors.New("agentloop: exclusive model execution and component routes are required"),
		})
		return
	}
	reply := gc.DeferResponse()
	if reply == nil {
		gc.Response(modelResult{
			err: errors.New("agentloop: model request requires an asynchronous request"),
		})
		return
	}
	endpoint, err := llmactor.NewClient(input.router, input.replyTimeout)
	if err != nil {
		reply.Response(modelResult{
			err: err,
		})
		return
	}
	next, err := owner.business.StartRequest()
	if err != nil {
		reply.Response(modelResult{
			err: err,
		})
		return
	}
	input.recoveryRoutes = slices.Clone(input.recoveryRoutes)
	invocation := &modelInvocation{
		input:  input,
		reply:  reply,
		models: endpoint,
	}
	owner.model = invocation
	owner.executeRequest(gc, invocation, next)
}

// executeRequest dispatches only the operation selected by ModelRequest.
func (owner *Grain) executeRequest(gc *goakt.GrainContext, invocation *modelInvocation, operation modelrequest.RequestOperation) {
	invocation.cancellation = nil
	switch input := operation.(type) {
	case modelrequest.RunAttempt:
		owner.executeModel(gc, invocation, input.Operation)
	case modelrequest.RequestFinished:
		invocation.outcome = input.Result

		if invocation.cleanup != nil && input.Result.StreamClosed {
			reply := invocation.cleanup
			invocation.cleanup = nil
			owner.model = nil
			owner.releaseResources(reply)
			return
		}
		owner.finishModel(invocation, input.Result.Err)
	case modelrequest.ResolveRecovery:
		id := middlewareactor.CallID(uuid.NewString())
		invocation.cancellation = &cancellationRequest{
			target: invocation.input.middleware,
			message: middlewareactor.CancelCall{
				CallID: id,
			},
		}
		owner.awaitRequest(
			gc, invocation, operation,
			gc.RequestGrain(
				invocation.input.middleware,
				middlewareactor.ResolveRequestError{
					CallID: id,
					Routes: invocation.input.recoveryRoutes,
					Input:  input.Input,
				},
				goakt.WithRequestTimeout(0),
				goakt.WithReentrancyMode(reentrancy.AllowAll),
			))
	default:
		owner.finishModel(invocation, errors.New("agentloop: unsupported model request operation"))
	}
}

func (owner *Grain) awaitRequest(
	gc *goakt.GrainContext,
	invocation *modelInvocation,
	operation modelrequest.RequestOperation,
	pending goakt.RequestCall,
) {
	completed := make(chan requestProgress, 1)
	stopped := owner.stopped
	if err := gc.PipeToSelf(func() (any, error) {
		select {
		case result := <-completed:
			return result, nil
		case <-stopped:
			return nil, nil
		}
	}); err != nil {
		owner.acceptRequest(gc, invocation, modelrequest.RequestFailed{
			Err: err,
		})
		return
	}
	pending.Then(func(payload any, failure error) {
		completed <- requestProgress{
			invocation: invocation,
			operation:  operation,
			payload:    payload,
			err:        failure,
		}
	})
}

func (owner *Grain) advanceRequest(gc *goakt.GrainContext, input requestProgress) {
	if owner.model != input.invocation {
		return
	}
	owner.acceptRequest(gc, input.invocation, requestResult(input))
}

func (owner *Grain) acceptRequest(gc *goakt.GrainContext, invocation *modelInvocation, result modelrequest.RequestResult) {
	next, err := owner.business.AdvanceRequest(result)
	if err != nil {
		owner.finishModel(invocation, err)
		return
	}
	owner.executeRequest(gc, invocation, next)
}

func requestResult(input requestProgress) modelrequest.RequestResult {
	if input.err != nil {
		return modelrequest.RequestFailed{
			Err: input.err,
		}
	}
	var failure error
	switch input.operation.(type) {
	case modelrequest.ResolveRecovery:
		if result, ok := input.payload.(agent.RequestErrorResolved); ok {
			if result.Err == nil {
				return modelrequest.RecoveryResolved{
					Action: result.Action,
				}
			}
			failure = result.Err
		}
	}
	if failure == nil {
		failure = errors.New("agentloop: invalid model request response")
	}
	return modelrequest.RequestFailed{
		Err: failure,
	}
}
