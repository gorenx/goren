package loopactor

import (
	"context"
	"errors"

	agentactor "github.com/gorenx/goren/agent/actor"
	"github.com/gorenx/goren/agentloop/internal/modelrequest"
	llmactor "github.com/gorenx/goren/llm/actor"
	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

type streamProgress struct {
	attempt   *modelInvocation
	operation modelrequest.Operation
	payload   any
	err       error
}

// executeModel maps a business operation to an asynchronous transport request.
func (owner *Grain) executeModel(
	gc *goakt.GrainContext,
	attempt *modelInvocation,
	operation modelrequest.Operation,
) {
	var message any
	switch input := operation.(type) {
	case modelrequest.ReadLog:
		message = sessionactor.Read{
			CreationID: owner.creationID,
		}
	case modelrequest.CommitEvents:
		message = sessionactor.Commit{
			CreationID: owner.creationID,
			Plan:       session.Batch(input.Draft),
		}
	default:
		owner.sendModelOperation(gc, attempt, operation)
		return
	}
	ctx, cancel := context.WithCancel(context.WithoutCancel(gc.Context()))
	attempt.cancel = cancel
	completed := make(chan streamProgress, 1)
	err := gc.PipeToSelf(
		func() (any, error) {
			select {
			case result := <-completed:
				return result, nil
			case <-ctx.Done():
				return streamProgress{
					attempt:   attempt,
					operation: operation,
					err:       context.Cause(ctx),
				}, nil
			}
		},
	)
	if err != nil {
		cancel()
		attempt.cancel = nil
		next, failure := owner.business.AdvanceRequest(
			modelrequest.AttemptAdvanced{
				Result: modelrequest.Failed{
					Err: err,
				},
			},
		)
		if failure != nil {
			owner.finishModel(attempt, failure)
			return
		}
		owner.executeRequest(gc, attempt, next)
		return
	}
	request := gc.RequestGrain(owner.conversation, message,
		goakt.WithRequestTimeout(0),
		goakt.WithReentrancyMode(reentrancy.StashNonReentrant),
	)
	request.Then(func(payload any, failure error) {
		completed <- streamProgress{
			attempt:   attempt,
			operation: operation,
			payload:   payload,
			err:       failure,
		}
	})
}

func (owner *Grain) advanceStream(gc *goakt.GrainContext, input streamProgress) {
	attempt := input.attempt
	if owner.model != attempt {
		return
	}
	if attempt.cancel != nil {
		attempt.cancel()
		attempt.cancel = nil
	}
	next, err := owner.business.AdvanceRequest(modelrequest.AttemptAdvanced{
		Result: streamResult(input),
	})
	if err != nil {
		owner.finishModel(attempt, err)
		return
	}
	owner.executeRequest(gc, attempt, next)
}

// streamResult only decodes the response for the operation that was dispatched.
func streamResult(input streamProgress) modelrequest.Result {
	if input.err != nil {
		return modelrequest.Failed{
			Err: input.err,
		}
	}
	var failure error
	switch input.operation.(type) {
	case modelrequest.ReadLog:
		result, ok := input.payload.(sessionactor.View)
		if ok && result.Err == nil {
			return modelrequest.LogRead{
				Snapshot: result.Snapshot,
			}
		}
		if ok {
			failure = result.Err
		}
	case modelrequest.CommitEvents:
		result, ok := input.payload.(sessionactor.Committed)
		if ok && result.Err == nil {
			return modelrequest.EventsCommitted{
				Write: result.Result,
			}
		}
		if ok {
			failure = result.Err
		}
	case modelrequest.Generate:
		if result, ok := input.payload.(llmactor.GenerationStarted); ok {
			return modelrequest.GenerationStarted{
				Config:      result.Config,
				Defaults:    result.Defaults,
				Context:     result.Context,
				RetryPolicy: result.RetryPolicy,
				Err:         result.Err,
			}
		}
	case modelrequest.ReadChunk:
		result, ok := input.payload.(llmactor.ChunkRead)
		if ok && result.Err == nil {
			return modelrequest.ChunkRead{
				Chunk: result.Chunk,
				Found: result.Found,
			}
		}
		if ok {
			failure = result.Err
		}
	case modelrequest.CloseStream:
		result, ok := input.payload.(llmactor.CallClosed)
		if ok && result.Err == nil {
			return modelrequest.StreamClosed{}
		}
		if ok {
			failure = result.Err
		}
	}
	if failure == nil {
		failure = errors.New("agentloop: invalid model operation response")
	}
	return modelrequest.Failed{
		Err: failure,
	}
}

func (owner *Grain) finishModel(attempt *modelInvocation, failure error) {
	defer owner.refreshIdle()
	if owner.model != attempt {
		return
	}
	if attempt.cancel != nil {
		attempt.cancel()
		attempt.cancel = nil
	}
	if attempt.cleanup != nil {
		attempt.cleanup.Response(agentactor.Closed{
			Err: failure,
		})
		attempt.cleanup = nil
	}
	if attempt.reply != nil {
		result := modelResult{
			err: failure,
		}
		result.finish = attempt.outcome.Finish
		if failure == nil {
			result.message = attempt.outcome.Message
		}
		attempt.reply.Response(result)
		attempt.reply = nil
	}
	if attempt.callID == "" && attempt.canceling == nil {
		owner.model = nil
	}
}
