package execution

import (
	"errors"

	"github.com/gorenx/goren/approval"
	"github.com/gorenx/goren/tools"
)

// Call owns the actual outputs of one staged execution. Its caller serializes
// methods; no method accepts a caller-authored body result or bypasses policy.
type Call struct {
	scheduler   *Scheduler
	preparation preparation
	dispatch    *dispatchResult
	result      tools.ToolExecutionResult
	failure     error
}

// Preparation exposes policy's current decision without the scheduling handle.
func (current *Call) Preparation() (tools.ScheduledToolStage, *tools.ApprovalRequest) {
	var request *tools.ApprovalRequest
	if current.preparation.Approval != nil {
		detached := *current.preparation.Approval
		request = &detached
	}
	return current.preparation.Stage, request
}

// ApplyApproval resumes this exact execution after the approval owner returns.
func (current *Call) ApplyApproval(outcome approval.Outcome) error {
	prepared, err := current.scheduler.applyApproval(current.preparation.Execution, outcome)
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
	dispatched, err := current.scheduler.dispatch(current.preparation.Execution)
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
	var result tools.ToolExecutionResult
	var err error
	switch current.preparation.Stage {
	case tools.ScheduledDispatch:
		if current.dispatch == nil {
			return errors.New("tools: body has not returned")
		}
		result, err = current.scheduler.finalize(current.preparation.Execution, current.dispatch.Result)
	case tools.ScheduledPostResult:
		result, err = current.scheduler.finalize(current.preparation.Execution, current.preparation.Result)
	case tools.ScheduledFinalResult:
		result = current.scheduler.finish(current.preparation.Execution, current.preparation.Result)
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
func (current *Call) Result() (tools.ToolExecutionResult, error) {
	if current.failure != nil {
		return nil, current.failure
	}
	if current.result == nil {
		return nil, errors.New("tools: execution has not settled")
	}
	return tools.CloneResult(current.result)
}
