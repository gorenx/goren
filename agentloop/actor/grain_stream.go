package loopactor

import (
	"context"
	"errors"

	"github.com/google/uuid"
	agentactor "github.com/gorenx/goren/agent/actor"
	"github.com/gorenx/goren/agentloop"
	"github.com/gorenx/goren/agentloop/internal/modelrequest"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

type requestModel struct {
	operation context.Context
	call      llm.PreparedLlmCall
}

type modelResult struct {
	message agentmessage.AssistantMessage
	finish  llm.FinishReason
	err     error
}

// modelAttempt retains only the business driver and its transport resources.
type modelAttempt struct {
	input      requestModel
	reader     *goakt.PID
	reply      *goakt.GrainReply
	business   *modelrequest.Attempt
	cancel     context.CancelFunc
	cancelRead context.CancelCauseFunc
	cleanup    *goakt.GrainReply
}

type streamProgress struct {
	attempt   *modelAttempt
	operation modelrequest.Operation
	payload   any
	err       error
}

func (owner *Grain) requestModel(gc *goakt.GrainContext, input requestModel) {
	if owner.model != nil || input.operation == nil || input.call == nil {
		gc.Response(modelResult{
			err: errors.New("agentloop: exclusive model transport, operation context and prepared call are required"),
		})
		return
	}
	prepared := agentloop.ModelInput{
		Config:   input.call.ConfigValue(),
		Defaults: input.call.AdapterDefaultsValue(),
	}
	if capacity, present := input.call.ContextValue(); present {
		prepared.Context = &capacity
	}
	business, err := owner.business.BeginModel(prepared)
	if err != nil {
		gc.Response(modelResult{
			err: err,
		})
		return
	}
	reply := gc.DeferResponse()
	if reply == nil {
		gc.Response(modelResult{
			err: errors.New("agentloop: model attempt requires an asynchronous request"),
		})
		return
	}
	operation, cancelRead := context.WithCancelCause(input.operation)
	input.operation = operation
	reader, err := gc.ActorSystem().Spawn(gc.Context(), "model-stream-"+uuid.NewString(), &streamReader{})
	if err != nil {
		cancelRead(nil)
		reply.Response(modelResult{
			err: err,
		})
		return
	}
	attempt := &modelAttempt{
		input:      input,
		reader:     reader,
		reply:      reply,
		cancelRead: cancelRead,
		business:   business,
	}
	owner.model = attempt
	next, err := business.Start()
	if err != nil {
		owner.finishModel(attempt, err)
		return
	}
	owner.executeModel(gc, attempt, next)
}

// executeModel maps a business operation to an asynchronous transport request.
func (owner *Grain) executeModel(gc *goakt.GrainContext, attempt *modelAttempt, operation modelrequest.Operation) {
	if outcome, ok := operation.(modelrequest.Completed); ok {
		owner.business.AcceptModel(outcome)
		if outcome.StreamClosed && attempt.reader != nil {
			if err := goakt.Tell(gc.Context(), attempt.reader, new(goakt.PoisonPill)); err != nil {
				owner.finishModel(attempt, errors.Join(outcome.Err, err))
				return
			}
			attempt.reader = nil
		}
		if attempt.cleanup != nil && outcome.StreamClosed {
			reply := attempt.cleanup
			attempt.cleanup = nil
			attempt.cancelRead(nil)
			owner.model = nil
			if owner.prompt != nil {
				owner.closePrompt(gc, reply)
			} else {
				owner.closeSession(gc, reply)
			}
			return
		}
		owner.finishModel(attempt, outcome.Err)
		return
	}
	var message any
	sessionRequest := false
	switch input := operation.(type) {
	case modelrequest.ReadLog:
		sessionRequest = true
		message = sessionactor.Read{
			CreationID: owner.creationID,
		}
	case modelrequest.CommitEvents:
		sessionRequest = true
		message = sessionactor.Commit{
			CreationID: owner.creationID,
			Plan:       session.Batch(input.Draft),
		}
	case modelrequest.OpenStream:
		message = openStream{
			operation: attempt.input.operation,
			call:      attempt.input.call,
			options:   input.Options,
		}
	case modelrequest.ReadChunk:
		message = readChunk{}
	case modelrequest.CloseStream:
		message = closeStream{}
	default:
		owner.finishModel(attempt, errors.New("agentloop: unsupported model operation"))
		return
	}
	ctx, cancel := context.WithCancel(context.WithoutCancel(gc.Context()))
	attempt.cancel = cancel
	completed := make(chan streamProgress, 1)
	if err := gc.PipeToSelf(func() (any, error) {
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
	}); err != nil {
		cancel()
		attempt.cancel = nil
		next, failure := attempt.business.Advance(modelrequest.Failed{
			Err: err,
		}, context.Cause(attempt.input.operation))
		if failure != nil {
			owner.finishModel(attempt, failure)
			return
		}
		owner.executeModel(gc, attempt, next)
		return
	}
	var request goakt.RequestCall
	if sessionRequest {
		request = gc.RequestGrain(owner.conversation, message,
			goakt.WithRequestTimeout(0),
			goakt.WithReentrancyMode(reentrancy.StashNonReentrant),
		)
	} else {
		request = gc.RequestActor(attempt.reader.Name(), message,
			goakt.WithRequestTimeout(0),
			goakt.WithReentrancyMode(reentrancy.AllowAll),
		)
	}
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
	next, err := attempt.business.Advance(streamResult(input), context.Cause(attempt.input.operation))
	if err != nil {
		owner.finishModel(attempt, err)
		return
	}
	owner.executeModel(gc, attempt, next)
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
	case modelrequest.OpenStream:
		result, ok := input.payload.(streamOpened)
		if ok && result.err == nil {
			return modelrequest.StreamOpened{}
		}
		if ok {
			failure = result.err
		}
	case modelrequest.ReadChunk:
		result, ok := input.payload.(chunkRead)
		if ok && result.err == nil {
			return modelrequest.ChunkRead{
				Chunk: result.chunk,
				Found: result.found,
			}
		}
		if ok {
			failure = result.err
		}
	case modelrequest.CloseStream:
		result, ok := input.payload.(streamClosed)
		if ok && result.err == nil {
			return modelrequest.StreamClosed{}
		}
		if ok {
			failure = result.err
		}
	}
	if failure == nil {
		failure = errors.New("agentloop: invalid model operation response")
	}
	return modelrequest.Failed{
		Err: failure,
	}
}

func (owner *Grain) finishModel(attempt *modelAttempt, failure error) {
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
		if attempt.business != nil {
			result.finish = attempt.business.Finish()
			if failure == nil {
				result.message = attempt.business.Message()
			}
		}
		attempt.reply.Response(result)
		attempt.reply = nil
	}
	if attempt.reader == nil {
		attempt.cancelRead(nil)
		owner.model = nil
	}
}
