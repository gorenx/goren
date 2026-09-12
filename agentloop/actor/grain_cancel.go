package loopactor

import (
	"errors"
	llmactor "github.com/gorenx/goren/llm/actor"

	"github.com/gorenx/goren/agent"
	agentactor "github.com/gorenx/goren/agent/actor"
	middlewareactor "github.com/gorenx/goren/agent/middleware/actor"
	"github.com/gorenx/goren/agentmessage"
	promptactor "github.com/gorenx/goren/systemprompt/actor"
	runtimeactor "github.com/gorenx/goren/tools/runtime/actor"
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
	owner.cancelOperations(gc, completed)
}

// cancelOperations delivers the stop intent to active capability requests.
func (owner *Grain) cancelOperations(gc *goakt.GrainContext, completed completeCancel) {
	if owner.maintenance != nil {
		owner.deliverCancellations(gc, []cancellationRequest{{
			target: owner.maintenance.worker,
			message: agentactor.CancelMaintenance{
				RequestID: owner.maintenance.requestID,
				Cause:     owner.business.Cancellation(),
			},
		}}, completed)
		return
	}
	if owner.callID != "" {
		// Close already cancels in-flight I/O and owns resource release. The
		// worker may have removed this call while its close reply is in transit.
		// Keep waiting for that reply instead of cancelling a released call.
		if owner.stream != nil {
			owner.finishCancellation(gc, completed)
			return
		}
		if owner.modelCancellation != nil {
			completed.err = errors.Join(completed.err, errors.New("agentloop: model cancellation acknowledgement is pending"))
			owner.finishCancellation(gc, completed)
			return
		}
		owner.modelCancellation = &completed
		owner.models.Cancel(gc, owner.callID, owner.business.Cancellation(), owner)
		return
	}
	requests := make([]cancellationRequest, 0, len(owner.cancellations)+len(owner.calls))
	for _, request := range owner.cancellations {
		requests = append(requests, request)
	}
	for identifier, phase := range owner.calls {
		if phase != toolCallActive {
			continue
		}
		requests = append(requests, cancellationRequest{
			target: owner.tools,
			message: runtimeactor.CancelCall{
				CreationID: owner.creationID,
				CallID:     agentmessage.CallID(identifier),
			},
		})
	}
	if len(requests) > 0 {
		owner.deliverCancellations(gc, requests, completed)
		return
	}

	owner.finishCancellation(gc, completed)
}

// completeCancel tells the mailbox to finish the cancellation request: commit
// its Inbox edit, if any, then reply with the cancellation delivery result.
// It does not finish the original Step, hook, or model execution.
type completeCancel struct {
	// closeCreation selects the lifecycle reply after cancellation delivery.
	closeCreation string
	reply         *goakt.GrainReply
	edit          agent.InboxEdit
	err           error
}

func (input completeCancel) respond(failure error) {
	if input.closeCreation != "" {
		input.reply.Response(agentactor.Prepared{Err: failure})
		return
	}
	input.reply.Response(Changed{Err: failure})
}

// cancellationRequest describes only an in-flight component's cancel endpoint.
// message is erased locally for the framework; component contracts remain named.
type cancellationRequest struct {
	target  *goakt.GrainIdentity
	message any
}

func (owner *Grain) deliverCancellations(gc *goakt.GrainContext, requests []cancellationRequest, input completeCancel) {
	completed := make(chan error, len(requests))
	stopped := owner.stopped
	err := gc.PipeToSelf(func() (any, error) {
		for range requests {
			select {
			case failure := <-completed:
				input.err = errors.Join(input.err, failure)
			case <-stopped:
				failure := errors.Join(input.err, errors.New("agentloop: stopped during cancellation delivery"))
				input.respond(failure)
				return nil, nil
			}
		}
		return input, nil
	})
	if err != nil {
		input.err = errors.Join(input.err, err)
		owner.finishCancellation(gc, input)
		return
	}
	for _, request := range requests {
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
					case agentactor.CancelMaintenance:
						result, ok := payload.(agentactor.MaintenanceCancelled)
						if !ok {
							failure = errors.New("agentloop: invalid maintenance cancellation acknowledgement")
						} else {
							failure = result.Err
						}
					case runtimeactor.CancelCall, runtimeactor.CancelConfiguration:
						result, ok := payload.(runtimeactor.Completion)
						if !ok {
							failure = errors.New("agentloop: invalid tool cancellation acknowledgement")
						} else {
							failure = result.Err
						}
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
}

func (owner *Grain) finishCancellation(gc *goakt.GrainContext, input completeCancel) {
	if input.closeCreation != "" {
		if input.err != nil {
			input.respond(input.err)
			return
		}
		// Run resumes only business-retained failures or accepted execution;
		// an operation already awaiting a capability result remains untouched.
		err := owner.business.Run(owner.services(gc))
		if err != nil {
			input.respond(err)
			return
		}
		owner.confirmClosePreparation(gc, closePreparation{
			creationID: input.closeCreation,
			reply:      input.reply,
		})
		return
	}
	if len(input.edit.Drafts) == 0 {
		input.respond(input.err)
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

// OnCancellationAccepted acknowledges delivery without completing the original call.
func (owner *Grain) OnCancellationAccepted(gc *goakt.GrainContext, input llmactor.CancellationAccepted) {
	if owner.modelCancellation == nil {
		return
	}
	completed := *owner.modelCancellation
	owner.modelCancellation = nil
	completed.err = errors.Join(completed.err, input.Err)
	owner.finishCancellation(gc, completed)
}
