package tools

import (
	"errors"

	"github.com/gorenx/goren/approval"
)

// Call owns the actual outputs of one staged execution. Its caller serializes
// methods; no method accepts a caller-authored body result or bypasses policy.
type Call struct {
	scheduler   *Scheduler
	preparation ScheduledToolPreparation
	dispatch    *ScheduledToolDispatch
	result      ToolExecutionResult
	failure     error
}

// Preparation exposes policy's current decision without the scheduling handle.
func (current *Call) Preparation() (ScheduledToolStage, *ApprovalRequest) {
	var request *ApprovalRequest
	if current.preparation.Approval != nil {
		detached := *current.preparation.Approval
		request = &detached
	}
	return current.preparation.Stage, request
}

// ResolveApproval resumes this exact execution after the approval owner returns.
func (current *Call) ResolveApproval(outcome approval.Outcome) error {
	prepared, err := current.scheduler.ResolveApproval(current.preparation.Execution, outcome)
	if err != nil {
		return err
	}
	current.preparation = prepared
	return nil
}

// Dispatch records the actual body outcome. An unconfirmed exit remains owned.
func (current *Call) Dispatch() error {
	if current.failure != nil {
		return current.failure
	}
	dispatched, err := current.scheduler.Dispatch(current.preparation.Execution)
	if err != nil {
		var delivery *DeliveryError
		if errors.As(err, &delivery) {
			current.failure = err
		}
		return err
	}
	current.dispatch = &dispatched
	return nil
}

// Finalize processes only retained policy or body output, never supplied results.
func (current *Call) Finalize() error {
	if current.failure != nil {
		return current.failure
	}
	if current.result != nil {
		return nil
	}
	var result ToolExecutionResult
	var err error
	switch current.preparation.Stage {
	case ScheduledDispatch:
		if current.dispatch == nil {
			return errors.New("tools: body has not returned")
		}
		result, err = current.scheduler.Finalize(current.preparation.Execution, current.dispatch.Result)
	case ScheduledPostResult:
		result, err = current.scheduler.Finalize(current.preparation.Execution, current.preparation.Result)
	case ScheduledFinalResult:
		result = current.scheduler.Finish(current.preparation.Execution, current.preparation.Result)
	default:
		return errors.New("tools: approval is not settled")
	}
	if err != nil {
		return err
	}
	current.result = result
	return nil
}

// Result confirms that final processing completed and detaches the retained value.
func (current *Call) Result() (ToolExecutionResult, error) {
	if current.failure != nil {
		return nil, current.failure
	}
	if current.result == nil {
		return nil, errors.New("tools: execution has not settled")
	}
	return current.result.cloneResult()
}
