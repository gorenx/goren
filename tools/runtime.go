package tools

import (
	"context"
	"errors"
	"fmt"

	"github.com/gorenx/goren/approval"
	"github.com/gorenx/goren/plugin"
)

// executionRuntime owns the staged Tool execution state machine. Policy evaluation,
// body dispatch, and result processing are delegated to focused collaborators.
type executionRuntime struct {
	registry   *registry
	policies   *policyEngine
	dispatcher *dispatcher
	results    *resultProcessor
}

func newRuntime(
	source plugin.Plugin,
	catalog *registry,
	approvals approval.Approval,
) *executionRuntime {
	bodyDispatcher := &dispatcher{
		source: source,
	}
	return &executionRuntime{
		registry: catalog,
		policies: &policyEngine{
			source:    source,
			registry:  catalog,
			approvals: approvals,
		},
		dispatcher: bodyDispatcher,
		results: &resultProcessor{
			source:     source,
			dispatcher: bodyDispatcher,
		},
	}
}

// Execute runs one Tool call through the complete staged pipeline.
func (coordinator *executionRuntime) Execute(
	requestContext context.Context,
	input ToolExecutionInput,
) ToolExecutionResult {
	preparation, err := coordinator.Prepare(requestContext, input)
	if err != nil {
		return errorResult(err)
	}
	switch preparation.Stage {
	case ScheduledDispatch:
		dispatched, dispatchErr := coordinator.Dispatch(preparation.Execution)
		if dispatchErr != nil {
			return errorResult(dispatchErr)
		}
		finalOutcome, finalizeErr := coordinator.Finalize(
			preparation.Execution,
			dispatched.Result,
		)
		if finalizeErr != nil {
			return errorResult(finalizeErr)
		}
		return finalOutcome
	case ScheduledPostResult:
		finalOutcome, finalizeErr := coordinator.Finalize(
			preparation.Execution,
			preparation.Result,
		)
		if finalizeErr != nil {
			return errorResult(finalizeErr)
		}
		return finalOutcome
	case ScheduledFinalResult:
		return coordinator.Finish(
			preparation.Execution,
			preparation.Result,
		)
	default:
		return errorResult(fmt.Errorf(
			"tools: unsupported scheduled stage %q",
			preparation.Stage,
		))
	}
}

// Prepare validates the call and runs ordered pre-dispatch policy without
// starting the Tool body.
func (coordinator *executionRuntime) Prepare(
	requestContext context.Context,
	input ToolExecutionInput,
) (ScheduledToolPreparation, error) {
	entry, _ := coordinator.registry.find(input.Name)
	var finalizer ContentFinalizer
	if entry != nil {
		finalizer = entry.definition.FinalizeContent
	}
	toolCall, err := createExecution(input)
	if requestContext == nil {
		requestContext = context.Background()
		toolCall.state = newScheduledExecutionState(
			coordinator,
			requestContext,
			entry,
			finalizer,
		)
		return ScheduledToolPreparation{
			Stage:     ScheduledFinalResult,
			Execution: toolCall,
			Result: errorResult(errors.New(
				"tools: execution context is nil",
			)),
		}, nil
	}
	toolCall.state = newScheduledExecutionState(
		coordinator,
		requestContext,
		entry,
		finalizer,
	)
	if err != nil {
		return finalPreparation(toolCall, errorResult(err)), nil
	}
	if requestContext.Err() != nil {
		return finalPreparation(toolCall, abortedResult(false)), nil
	}
	evaluation, err := coordinator.policies.evaluate(
		requestContext,
		toolCall,
	)
	if err != nil {
		return finalPreparation(toolCall, errorResult(err)), nil
	}
	if evaluation.approvalCancelled && requestContext.Err() != nil {
		return postPreparation(toolCall, abortedResult(false)), nil
	}
	if evaluation.denialReason != "" {
		return postPreparation(
			toolCall,
			deniedResult(evaluation.denialReason),
		), nil
	}
	if requestContext.Err() != nil {
		return postPreparation(toolCall, abortedResult(false)), nil
	}
	toolCall.state.prepareForDispatch()
	return ScheduledToolPreparation{
		Stage:     ScheduledDispatch,
		Execution: toolCall,
	}, nil
}

// Dispatch runs the around-dispatch chain and Tool body.
func (coordinator *executionRuntime) Dispatch(
	toolCall ToolExecution,
) (ScheduledToolDispatch, error) {
	state, err := coordinator.executionState(toolCall)
	if err != nil {
		return ScheduledToolDispatch{}, err
	}
	if err := state.beginDispatch(); err != nil {
		return ScheduledToolDispatch{}, err
	}
	defer state.completeDispatch()
	outcome, err := coordinator.dispatcher.dispatch(
		state.requestContext,
		toolCall,
		state.entry,
	)
	if err != nil {
		return ScheduledToolDispatch{
			Result: errorResult(err),
		}, nil
	}
	return ScheduledToolDispatch{
		Result: outcome,
	}, nil
}

// Finalize applies ordered post policy, cancellation, final content, and the
// final result Event.
func (coordinator *executionRuntime) Finalize(
	toolCall ToolExecution,
	outcome ToolExecutionResult,
) (ToolExecutionResult, error) {
	state, err := coordinator.executionState(toolCall)
	if err != nil {
		return nil, err
	}
	if err := state.beginFinalize(); err != nil {
		return nil, err
	}
	defer state.completeFinalization()
	postOutcome, err := coordinator.results.post(
		state.requestContext,
		toolCall,
		state.entry,
		outcome,
	)
	if err != nil {
		return coordinator.results.finish(
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
	return coordinator.results.finish(
		state.requestContext,
		toolCall,
		postOutcome,
		state.finalizer,
	), nil
}

// Finish materializes a terminal staged result without post-execute policy.
func (coordinator *executionRuntime) Finish(
	toolCall ToolExecution,
	outcome ToolExecutionResult,
) ToolExecutionResult {
	state, err := coordinator.executionState(toolCall)
	if err != nil {
		return errorResult(err)
	}
	if err := state.beginFinish(); err != nil {
		return errorResult(err)
	}
	defer state.completeFinalization()
	return coordinator.results.finish(
		state.requestContext,
		toolCall,
		outcome,
		state.finalizer,
	)
}

func (coordinator *executionRuntime) executionState(
	toolCall ToolExecution,
) (*scheduledExecutionState, error) {
	state := toolCall.state
	if state == nil || state.coordinator != coordinator ||
		toolCall.Token.token == nil {
		return nil, errors.New(
			"tools: execution was not prepared by this runtime",
		)
	}
	return state, nil
}

func finalPreparation(
	toolCall ToolExecution,
	outcome ToolExecutionResult,
) ScheduledToolPreparation {
	toolCall.state.prepareForFinish()
	return ScheduledToolPreparation{
		Stage:     ScheduledFinalResult,
		Execution: toolCall,
		Result:    outcome,
	}
}

func postPreparation(
	toolCall ToolExecution,
	outcome ToolExecutionResult,
) ScheduledToolPreparation {
	toolCall.state.prepareForPost()
	return ScheduledToolPreparation{
		Stage:     ScheduledPostResult,
		Execution: toolCall,
		Result:    outcome,
	}
}
