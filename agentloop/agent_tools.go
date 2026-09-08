package agentloop

import (
	"errors"

	"github.com/gorenx/goren/agent"
	stepstate "github.com/gorenx/goren/agentloop/internal/step"
	"github.com/gorenx/goren/agentloop/internal/toolbatch"
)

// CompleteTools prepares Step settlement after the current local batch closes.
// The caller associates the batch with this execution. Ordinary tool failure
// results belong to the model conversation; failure is a batch execution error.
// Pending next-step input is preserved, including when a tool concludes the Step.
func (subject *ReactLoopAgent) CompleteTools(batch *toolbatch.ToolBatch, failure error) (EndStep, error) {
	if subject.step == nil || subject.step.StateValue() != stepstate.StateTooling {
		return EndStep{}, errors.New("agentloop: tool completion requires a current tooling Step")
	}
	if !batch.IsComplete() {
		return EndStep{}, errors.New("agentloop: tool batch must close before Step settlement")
	}
	var cancelled agent.Cancellation
	isCancellation := errors.As(failure, &cancelled)
	switch batch.StopReason() {
	case toolbatch.Canceled:
		if !isCancellation {
			return EndStep{}, errors.New("agentloop: canceled tool batch requires its cancellation error")
		}
	case toolbatch.ExecutionFailed:
		if failure == nil {
			return EndStep{}, errors.New("agentloop: failed tool batch requires its execution error")
		}
	}
	conclusion := stepstate.StepResultContinue
	switch {
	case isCancellation:
		conclusion = stepstate.StepResultAborted
	case failure != nil:
		conclusion = stepstate.StepResultError
	case batch.StopsModelContinuation():
		conclusion = stepstate.StepResultCompleted
	}
	return subject.endStep(conclusion, failure)
}
