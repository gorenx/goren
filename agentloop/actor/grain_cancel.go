package loopactor

import (
	"context"
	"errors"
	"github.com/google/uuid"
	llmactor "github.com/gorenx/goren/llm/actor"
	"time"

	"github.com/gorenx/goren/agent"
	middlewareactor "github.com/gorenx/goren/agent/middleware/actor"
	promptactor "github.com/gorenx/goren/systemprompt/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

func (owner *Grain) cancel(gc *goakt.GrainContext, input Cancel) {
	reply := owner.inboxReply(gc, input.CreationID)
	if reply == nil {
		return
	}
	edit, err := owner.business.Cancel(input.Cause, input.Options)
	completed := completeCancel{
		reply: reply,
		edit:  edit,
		err:   err,
	}
	if owner.model != nil && owner.model.callID != "" {
		if owner.model.canceling != nil {
			completed.err = errors.Join(completed.err, errors.New("agentloop: model cancellation acknowledgement is pending"))
			owner.finishCancellation(gc, completed)
			return
		}
		id := llmactor.RequestID(uuid.NewString())
		owner.model.canceling = &modelCancellation{
			id:         id,
			completion: completed,
		}
		if failure := owner.awaitModelCancellation(gc, owner.model); failure != nil {
			owner.model.canceling.cancel()
			owner.model.canceling = nil
			completed.err = errors.Join(completed.err, failure)
			owner.finishCancellation(gc, completed)
			return
		}
		if failure := goakt.Tell(gc.Context(), owner.model.input.router, goakt.NewBroadcast(llmactor.CancelCall{
			Delivery: llmactor.Delivery{
				CallID:    owner.model.callID,
				RequestID: id,
				ReplyTo:   gc.Self(),
			},
			Cause: owner.business.Cancellation(),
		})); failure != nil {
			owner.model.canceling.cancel()
			owner.model.canceling = nil
			completed.err = errors.Join(completed.err, failure)
			owner.finishCancellation(gc, completed)
		}
		return
	}
	if owner.model != nil && owner.model.cancellation != nil {
		owner.deliverCancellation(gc, *owner.model.cancellation, completed)
		return
	}
	if owner.preparation != nil && owner.preparation.cancellation != nil {
		owner.deliverCancellation(gc, *owner.preparation.cancellation, completed)
		return
	}
	if owner.stopping != nil {
		owner.deliverCancellation(gc, cancellationRequest{
			target: owner.stopping.middleware,
			message: middlewareactor.CancelCall{
				CallID: owner.stopping.id,
				Cause:  owner.business.Cancellation(),
			},
		}, completed)
		return
	}
	owner.finishCancellation(gc, completed)
}

// completeCancel tells the mailbox to finish the cancellation request: commit
// its Inbox edit, if any, then reply with the cancellation delivery result.
// It does not finish the original Step, hook, or model execution.
type completeCancel struct {
	reply *goakt.GrainReply
	edit  agent.InboxEdit
	err   error
}

// cancellationRequest describes only an in-flight component's cancel endpoint.
// message is erased locally for the framework; component contracts remain named.
type cancellationRequest struct {
	target  *goakt.GrainIdentity
	message any
}

func (owner *Grain) deliverCancellation(gc *goakt.GrainContext, request cancellationRequest, input completeCancel) {
	completed := make(chan error, 1)
	stopped := owner.stopped
	if err := gc.PipeToSelf(func() (any, error) {
		select {
		case failure := <-completed:
			input.err = errors.Join(input.err, failure)
			return input, nil
		case <-stopped:
			input.reply.Response(Changed{
				Err: errors.Join(input.err, errors.New("agentloop: stopped during cancellation delivery")),
			})
			return nil, nil
		}
	}); err != nil {
		input.err = errors.Join(input.err, err)
		owner.finishCancellation(gc, input)
		return
	}
	message := request.message
	if cancellation, ok := message.(middlewareactor.CancelCall); ok {
		cancellation.Cause = owner.business.Cancellation()
		message = cancellation
	}
	gc.RequestGrain(
		request.target, message,
		goakt.WithReentrancyMode(reentrancy.StashNonReentrant),
	).Then(
		func(payload any, failure error) {
			if failure == nil {
				switch message.(type) {
				case promptactor.CancelAssembly:
					result, ok := payload.(promptactor.Completion)
					if !ok {
						failure = errors.New("agentloop: invalid Prompt cancellation acknowledgement")
					} else {
						failure = result.Err
					}
				case middlewareactor.CancelCall:
					result, ok := payload.(middlewareactor.Completion)
					if !ok {
						failure = errors.New("agentloop: invalid middleware cancellation acknowledgement")
					} else {
						failure = result.Err
					}
				}
			}
			completed <- failure
		},
	)
}

func (owner *Grain) finishCancellation(gc *goakt.GrainContext, input completeCancel) {
	if len(input.edit.Drafts) == 0 {
		input.reply.Response(Changed{
			Err: input.err,
		})
		return
	}
	owner.commitInbox(
		gc,
		input.reply,
		agent.Admission{
			Edit: input.edit,
		},
		input.err,
	)
}

// modelCancellation retains only the reply awaiting LLM cancellation delivery.
// The original model operation and resource closure finish independently.
type modelCancellation struct {
	id         llmactor.RequestID
	completion completeCancel
	cancel     context.CancelFunc
}

func (owner *Grain) modelCancellationResult(gc *goakt.GrainContext, input llmactor.CancellationAccepted) {
	gc.NoErr()
	current := owner.model
	if current == nil || current.canceling == nil || current.canceling.id != input.RequestID {
		return
	}
	completed := current.canceling.completion
	current.canceling.cancel()
	current.canceling = nil
	completed.err = errors.Join(completed.err, input.Err)
	owner.finishCancellation(gc, completed)
	if current.callID == "" && current.reply == nil && current.cleanup == nil {
		owner.model = nil
		owner.refreshIdle()
	}
}

type modelCancellationExpired struct{ id llmactor.RequestID }

func (owner *Grain) awaitModelCancellation(gc *goakt.GrainContext, current *modelInvocation) error {
	lifetime, cancel := context.WithCancel(context.WithoutCancel(gc.Context()))
	current.canceling.cancel = cancel
	expired := modelCancellationExpired{id: current.canceling.id}
	timeout := current.input.replyTimeout
	stopped := owner.stopped
	return gc.PipeToSelf(func() (any, error) {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		select {
		case <-timer.C:
			return expired, nil
		case <-lifetime.Done():
			return nil, nil
		case <-stopped:
			return nil, nil
		}
	})
}
