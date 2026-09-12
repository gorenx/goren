package execution

import (
	"errors"
	"github.com/gorenx/goren/tools"
)

// Dispatch runs the around-dispatch chain and Tool body.
func (current *Call) dispatchBody() (dispatchResult, error) {
	toolCall := current.preparation.Execution
	state, err := current.scheduler.executionState(toolCall)
	if err != nil {
		return dispatchResult{}, err
	}
	err = state.beginDispatch()
	if err != nil {
		return dispatchResult{}, err
	}
	defer state.completeDispatch()
	outcome, err := current.invoke(
		state.requestContext,
		toolCall,
		state.entry,
	)
	if err != nil {
		var unconfirmed *DeliveryError
		if errors.As(err, &unconfirmed) {
			return dispatchResult{}, unconfirmed
		}
		return dispatchResult{
			Result: errorResult(err),
		}, nil
	}
	return dispatchResult{
		Result: outcome,
	}, nil
}

// Finalize applies ordered post policy, cancellation, final content, and the
// final result Event.
func (current *Call) finalize(
	outcome tools.ToolExecutionResult,
) (tools.ToolExecutionResult, error) {
	toolCall := current.preparation.Execution
	state, err := current.scheduler.executionState(toolCall)
	if err != nil {
		return nil, err
	}
	err = state.beginFinalize()
	if err != nil {
		return nil, err
	}
	defer state.completeFinalization()
	postOutcome, err := current.post(
		state.requestContext,
		toolCall,
		state.entry,
		outcome,
	)
	if err != nil {
		return current.finishResult(
			state.requestContext,
			toolCall,
			errorResult(err),
			state.finalizer,
		), nil
	}
	stateSnapshot := state.snapshot()
	if state.requestContext.Err() != nil && !postOutcome.Failed() {
		postOutcome = abortedResult(
			stateSnapshot.bodyInvoked,
			postOutcome,
		)
	}
	return current.finishResult(
		state.requestContext,
		toolCall,
		postOutcome,
		state.finalizer,
	), nil
}

// Finish materializes a terminal staged result without post-execute policy.
func (current *Call) finish(
	outcome tools.ToolExecutionResult,
) tools.ToolExecutionResult {
	toolCall := current.preparation.Execution
	state, err := current.scheduler.executionState(toolCall)
	if err != nil {
		return errorResult(err)
	}
	err = state.beginFinish()
	if err != nil {
		return errorResult(err)
	}
	defer state.completeFinalization()
	return current.finishResult(
		state.requestContext,
		toolCall,
		outcome,
		state.finalizer,
	)
}
