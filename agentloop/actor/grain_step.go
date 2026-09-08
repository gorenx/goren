package loopactor

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/gorenx/goren/agent"
	middlewareactor "github.com/gorenx/goren/agent/middleware/actor"
	"github.com/gorenx/goren/agentloop"
	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	promptactor "github.com/gorenx/goren/systemprompt/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

// prepareStep is internal orchestration; public inputs cannot skip Turn admission.
type prepareStep struct {
	middleware *goakt.GrainIdentity
	routes     []middlewareactor.Route
}

type stepPrepared struct {
	entered bool
	err     error
}

// stepPreparation retains request resources and detached upstream data only.
// Legal preparation transitions are owned by ReactLoopAgent.
type stepPreparation struct {
	cancellation *cancellationRequest
	reply        *goakt.GrainReply
	cancel       context.CancelFunc
	id           string
	middleware   *goakt.GrainIdentity
	routes       []middlewareactor.Route
	failure      error
	snapshot     session.Snapshot
}

type preparationFailed struct {
	operation *stepPreparation
	err       error
}

type inputClaimed struct {
	operation *stepPreparation
	result    sessionactor.Committed
}

type contextRead struct {
	operation *stepPreparation
	result    sessionactor.View
}

type promptAssembled struct {
	operation *stepPreparation
	result    promptactor.Assembled
}

type admissionResolved struct {
	operation *stepPreparation
	result    agent.PreStepResolved
}

type stepStarted struct {
	operation *stepPreparation
	result    sessionactor.Committed
}

type messageCommitted struct {
	operation *stepPreparation
	result    sessionactor.Committed
}

func (owner *Grain) startStep(gc *goakt.GrainContext, input prepareStep) {
	if owner.preparation != nil || owner.model != nil || owner.ending != nil || owner.conversation == nil || owner.prompt == nil || input.middleware == nil {
		gc.Response(stepPrepared{
			err: errPreparationUnavailable,
		})
		return
	}
	reply := gc.DeferResponse()
	if reply == nil {
		gc.Response(stepPrepared{
			err: errors.New("agentloop: Step preparation requires an asynchronous request"),
		})
		return
	}
	edit, err := owner.business.PrepareInput()
	if err != nil {
		reply.Response(stepPrepared{
			err: err,
		})
		return
	}
	operation := &stepPreparation{
		reply:      reply,
		id:         uuid.NewString(),
		middleware: input.middleware,
		routes:     append([]middlewareactor.Route(nil), input.routes...),
	}
	owner.preparation = operation
	if len(edit.Drafts) == 0 {
		owner.readStepContext(gc, operation)
		return
	}
	owner.requestPreparation(gc, operation, owner.conversation, sessionactor.Commit{
		CreationID: owner.creationID,
		Plan:       session.Batch(edit.Drafts...),
	}, false, func(payload any, failure error) any {
		if failure != nil {
			return inputClaimed{
				operation: operation,
				result:    sessionactor.Committed{Err: failure},
			}
		}
		result, ok := payload.(sessionactor.Committed)
		if !ok {
			return inputClaimed{
				operation: operation,
				result:    sessionactor.Committed{Err: errors.New("agentloop: invalid input claim response")},
			}
		}
		return inputClaimed{
			operation: operation,
			result:    result,
		}
	})
}

var errPreparationUnavailable = errors.New("agentloop: Step preparation requires idle transport and initialized routes")

// requestPreparation sends a named external request and resumes through a named
// mailbox message. The decoder is stateless framing, not a business continuation.
func (owner *Grain) requestPreparation(
	gc *goakt.GrainContext,
	operation *stepPreparation,
	target *goakt.GrainIdentity,
	message any, reentrant bool,
	decode func(any, error) any,
) {
	operation.cancellation = nil
	switch request := message.(type) {
	case promptactor.Assemble:
		operation.cancellation = &cancellationRequest{
			target: target,
			message: promptactor.CancelAssembly{
				ID: request.ID,
			},
		}
	case middlewareactor.ResolvePreStep:
		operation.cancellation = &cancellationRequest{
			target: target,
			message: middlewareactor.CancelCall{
				CallID: request.CallID,
			},
		}
	}
	ctx, cancel := context.WithCancel(context.WithoutCancel(gc.Context()))
	operation.cancel = cancel
	completed := make(chan any, 1)
	if err := gc.PipeToSelf(func() (any, error) {
		select {
		case result := <-completed:
			return result, nil
		case <-ctx.Done():
			return preparationFailed{
				operation: operation,
				err:       context.Cause(ctx),
			}, nil
		}
	}); err != nil {
		owner.finishPreparation(operation, false, err)
		return
	}
	mode := reentrancy.StashNonReentrant
	if reentrant {
		mode = reentrancy.AllowAll
	}
	gc.RequestGrain(
		target,
		message,
		goakt.WithRequestTimeout(0),
		goakt.WithReentrancyMode(mode),
	).Then(
		func(payload any, failure error) {
			completed <- decode(payload, failure)
		},
	)
}

func (owner *Grain) acceptPreparation(operation *stepPreparation) bool {
	if owner.preparation != operation {
		return false
	}
	if operation.cancel != nil {
		operation.cancel()
		operation.cancel = nil
	}
	return true
}

func (owner *Grain) checkPreparationCancellation(operation *stepPreparation) bool {
	if failure := owner.business.Cancellation(); failure != nil {
		owner.finishPreparation(operation, false, failure)
		return false
	}
	return true
}

func (owner *Grain) claimConfirmed(gc *goakt.GrainContext, input inputClaimed) {
	operation := input.operation
	if !owner.acceptPreparation(operation) {
		return
	}
	if input.result.Err != nil {
		owner.recoverInput(gc, operation, input.result.Err)
		return
	}
	if err := owner.business.ConfirmInput(input.result.Result); err != nil {
		owner.recoverInput(gc, operation, err)
		return
	}
	if !owner.checkPreparationCancellation(operation) {
		return
	}
	owner.readStepContext(gc, operation)
}

func (owner *Grain) readStepContext(gc *goakt.GrainContext, operation *stepPreparation) {
	owner.requestPreparation(gc,
		operation,
		owner.conversation,
		sessionactor.Read{CreationID: owner.creationID},
		false,
		func(payload any, failure error) any {
			if failure != nil {
				return preparationFailed{
					operation: operation,
					err:       failure,
				}
			}
			result, ok := payload.(sessionactor.View)
			if !ok {
				return preparationFailed{
					operation: operation,
					err:       errors.New("agentloop: invalid Step Session response"),
				}
			}
			return contextRead{
				operation: operation,
				result:    result,
			}
		},
	)
}

func (owner *Grain) preparePrompt(gc *goakt.GrainContext, input contextRead) {
	operation := input.operation
	if !owner.acceptPreparation(operation) {
		return
	}
	if input.result.Err != nil {
		owner.finishPreparation(operation, false, input.result.Err)
		return
	}
	if !owner.checkPreparationCancellation(operation) {
		return
	}
	operation.snapshot = input.result.Snapshot
	owner.requestPreparation(gc, operation, owner.prompt, promptactor.Assemble{
		ID:           operation.id,
		ToolsVersion: owner.business.ToolsVersion(),
	}, true, func(payload any, failure error) any {
		if failure != nil {
			return preparationFailed{
				operation: operation,
				err:       failure,
			}
		}
		result, ok := payload.(promptactor.Assembled)
		if !ok {
			return preparationFailed{
				operation: operation,
				err:       errors.New("agentloop: invalid Prompt response"),
			}
		}
		return promptAssembled{
			operation: operation,
			result:    result,
		}
	})
}

func (owner *Grain) resolveStepAdmission(gc *goakt.GrainContext, input promptAssembled) {
	operation := input.operation
	if !owner.acceptPreparation(operation) {
		return
	}
	if input.result.Err != nil {
		owner.finishPreparation(operation, false, input.result.Err)
		return
	}
	if input.result.ID != operation.id {
		owner.finishPreparation(operation, false, errors.New("agentloop: Prompt response identity mismatch"))
		return
	}
	if !owner.checkPreparationCancellation(operation) {
		return
	}
	proposal, err := owner.business.PreparePreStep(agentloop.Preparation{
		Snapshot:     operation.snapshot,
		Prompt:       input.result.Prompt,
		ToolsVersion: owner.business.ToolsVersion(),
	})
	if err != nil {
		owner.finishPreparation(operation, false, err)
		return
	}
	operation.snapshot = session.Snapshot{}
	owner.requestPreparation(gc, operation, operation.middleware, middlewareactor.ResolvePreStep{
		CallID:  middlewareactor.CallID(operation.id),
		Routes:  operation.routes,
		Input:   proposal.Input,
		Default: proposal.Default,
	}, true, func(payload any, failure error) any {
		if failure != nil {
			return preparationFailed{
				operation: operation,
				err:       failure,
			}
		}
		result, ok := payload.(agent.PreStepResolved)
		if !ok {
			return preparationFailed{
				operation: operation,
				err:       errors.New("agentloop: invalid pre-step response"),
			}
		}
		return admissionResolved{
			operation: operation,
			result:    result,
		}
	})
}

func (owner *Grain) openStep(gc *goakt.GrainContext, input admissionResolved) {
	operation := input.operation
	if !owner.acceptPreparation(operation) {
		return
	}
	if input.result.Err != nil {
		owner.finishPreparation(operation, false, input.result.Err)
		return
	}
	if !owner.checkPreparationCancellation(operation) {
		return
	}
	draft, err := owner.business.PrepareStep(input.result.Decision)
	if err != nil {
		owner.finishPreparation(operation, false, err)
		return
	}
	if draft == nil {
		owner.finishPreparation(operation, false, nil)
		return
	}
	owner.requestPreparation(gc, operation, owner.conversation, sessionactor.Commit{
		CreationID: owner.creationID,
		Plan:       session.Batch(*draft),
	}, false, func(payload any, failure error) any {
		if failure != nil {
			return stepStarted{
				operation: operation,
				result: sessionactor.Committed{
					Err: failure,
				},
			}
		}
		result, ok := payload.(sessionactor.Committed)
		if !ok {
			return stepStarted{
				operation: operation,
				result: sessionactor.Committed{
					Err: errors.New("agentloop: invalid Step start response"),
				},
			}
		}
		return stepStarted{
			operation: operation,
			result:    result,
		}
	})
}

func (owner *Grain) stepConfirmed(gc *goakt.GrainContext, input stepStarted) {
	operation := input.operation
	if !owner.acceptPreparation(operation) {
		return
	}
	if input.result.Err != nil {
		owner.recoverStep(gc, operation, input.result.Err)
		return
	}
	if err := owner.business.ConfirmStep(input.result.Result); err != nil {
		owner.recoverStep(gc, operation, err)
		return
	}
	if !owner.checkPreparationCancellation(operation) {
		return
	}
	owner.commitStepMessage(gc, operation)
}

func (owner *Grain) commitStepMessage(gc *goakt.GrainContext, operation *stepPreparation) {
	draft, err := owner.business.MessageDraft()
	if err != nil {
		owner.finishPreparation(operation, false, err)
		return
	}
	if draft == nil {
		err := owner.business.BeginRequest()
		owner.finishPreparation(operation, err == nil, err)
		return
	}
	owner.requestPreparation(gc, operation, owner.conversation, sessionactor.Commit{
		CreationID: owner.creationID,
		Plan:       session.Batch(*draft),
	}, false, func(payload any, failure error) any {
		if failure != nil {
			return preparationFailed{
				operation: operation,
				err:       failure,
			}
		}
		result, ok := payload.(sessionactor.Committed)
		if !ok {
			return preparationFailed{
				operation: operation,
				err:       errors.New("agentloop: invalid message commit response"),
			}
		}
		return messageCommitted{
			operation: operation,
			result:    result,
		}
	})
}

func (owner *Grain) messageConfirmed(gc *goakt.GrainContext, input messageCommitted) {
	operation := input.operation
	if !owner.acceptPreparation(operation) {
		return
	}
	if input.result.Err != nil {
		owner.finishPreparation(operation, false, input.result.Err)
		return
	}
	if err := owner.business.ConfirmMessage(input.result.Result); err != nil {
		owner.finishPreparation(operation, false, err)
		return
	}
	if !owner.checkPreparationCancellation(operation) {
		return
	}
	owner.commitStepMessage(gc, operation)
}

func (owner *Grain) finishPreparation(operation *stepPreparation, entered bool, failure error) {
	if owner.preparation != operation {
		return
	}
	if operation.cancel != nil {
		operation.cancel()
		operation.cancel = nil
	}
	if failure != nil {
		// ErrState means the business owner retained an unresolved write or a Step;
		// these must be reconciled or settled, never discarded by transport cleanup.
		if err := owner.business.AbandonPreparation(); err != nil && !errors.Is(err, agentloop.ErrState) {
			failure = errors.Join(failure, err)
		}
	}
	owner.preparation = nil
	owner.refreshIdle()
	operation.reply.Response(stepPrepared{
		entered: entered,
		err:     failure,
	})
}

// stepRecovered follows the failed start request on the same Session owner.
type stepRecovered struct {
	operation *stepPreparation
	result    sessionactor.View
}

func (owner *Grain) recoverStep(gc *goakt.GrainContext, operation *stepPreparation, failure error) {
	operation.failure = failure
	owner.requestPreparation(gc, operation, owner.conversation, sessionactor.Read{
		CreationID: owner.creationID,
	}, false, func(payload any, transportErr error) any {
		result, ok := payload.(sessionactor.View)
		if transportErr != nil {
			result.Err = transportErr
		} else if !ok {
			result.Err = errors.New("agentloop: invalid Step recovery response")
		}
		return stepRecovered{
			operation: operation,
			result:    result,
		}
	})
}

func (owner *Grain) reconcileStep(gc *goakt.GrainContext, input stepRecovered) {
	operation := input.operation
	if !owner.acceptPreparation(operation) {
		return
	}
	failure := input.result.Err
	if failure == nil {
		_, failure = owner.business.ReconcileStepStart(input.result.Snapshot)
	}
	owner.finishPreparation(operation, false, errors.Join(operation.failure, owner.business.Cancellation(), failure))
}

type inputRecovered struct {
	operation *stepPreparation
	result    sessionactor.View
}

func (owner *Grain) recoverInput(gc *goakt.GrainContext, operation *stepPreparation, failure error) {
	operation.failure = failure
	owner.requestPreparation(gc, operation, owner.conversation, sessionactor.Read{
		CreationID: owner.creationID,
	}, false, func(payload any, transportErr error) any {
		result, ok := payload.(sessionactor.View)
		if transportErr != nil {
			result.Err = transportErr
		} else if !ok {
			result.Err = errors.New("agentloop: invalid claim recovery response")
		}
		return inputRecovered{
			operation: operation,
			result:    result,
		}
	})
}

func (owner *Grain) reconcileInput(gc *goakt.GrainContext, input inputRecovered) {
	operation := input.operation
	if !owner.acceptPreparation(operation) {
		return
	}
	failure := input.result.Err
	if failure == nil {
		_, failure = owner.business.ReconcileInput(input.result.Snapshot)
	}
	owner.finishPreparation(operation, false, errors.Join(operation.failure, owner.business.Cancellation(), failure))
}
