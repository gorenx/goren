package tools

import (
	"context"
	"errors"
	"fmt"

	"github.com/gorenx/goren/approval"
)

// Scheduler owns the staged Tool execution state machine. Policy evaluation,
// body dispatch, and result processing are delegated to focused collaborators.
type Scheduler struct {
	definitions map[string]*registeredTool
	pipeline    *pipeline
	policies    *policyEngine
	dispatcher  *dispatcher
	results     *resultProcessor
}

// Invoker calls an already-selected body without owning policy or result handling.
// A returned error means body exit is unconfirmed; never fabricate a Tool result.
type Invoker interface {
	Invoke(context.Context, Invocation) (BodyResult, error)
}

// Policies fixes the business collaborators used by prepared executions.
// Borrowed implementations must remain valid until those executions finish.
type Policies struct {
	Before    PrePolicy
	After     PostPolicy
	Execution ExecuteMiddleware
	Results   ResultObserver
	Guards    []ToolGuard
}

// NewScheduler captures executable descriptions independently of registration and Actors.
func NewScheduler(descriptions []Descriptor, policies Policies, bodies Invoker) (*Scheduler, error) {
	if bodies == nil {
		return nil, errors.New("tools: body invoker is required")
	}
	entries := make(map[string]*registeredTool, len(descriptions))
	for _, description := range descriptions {
		entry, err := compileDescriptor(description.definition())
		if err != nil {
			return nil, err
		}
		if _, found := entries[description.Name]; found {
			return nil, errors.New("tools: duplicate execution descriptor")
		}
		entries[description.Name] = entry
	}
	for _, guard := range policies.Guards {
		if guard == nil {
			return nil, errors.New("tools: guard is required")
		}
	}
	return newScheduler(entries, &pipeline{
		options: Options{
			Before:    policies.Before,
			After:     policies.After,
			Execution: policies.Execution,
			Results:   policies.Results,
		},
	}, policies.Guards, bodies), nil
}

func newScheduler(
	entries map[string]*registeredTool,
	stages *pipeline,
	guards []ToolGuard,
	bodies Invoker,
) *Scheduler {
	bodyDispatcher := &dispatcher{
		bodies: bodies,
	}
	return &Scheduler{
		definitions: entries,
		pipeline:    stages,
		policies: &policyEngine{
			guards: append([]ToolGuard(nil), guards...),
		},
		dispatcher: bodyDispatcher,
		results: &resultProcessor{
			dispatcher: bodyDispatcher,
		},
	}
}

// Execute runs one Tool call through the complete staged pipeline.
func (coordinator *Scheduler) Execute(
	requestContext context.Context,
	input ToolExecutionInput,
) ToolExecutionResult {
	preparation, err := coordinator.Prepare(requestContext, input)
	if err != nil {
		return errorResult(err)
	}
	switch preparation.Stage {
	case ScheduledApproval:
		blocked := postPreparation(
			preparation.Execution,
			deniedResult(fallbackAskReason(preparation.Execution.Name, preparation.Approval.Reason)),
		)
		result, err := coordinator.Finalize(blocked.Execution, blocked.Result)
		if err != nil {
			return errorResult(err)
		}
		return result
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
func (coordinator *Scheduler) Prepare(
	requestContext context.Context,
	input ToolExecutionInput,
) (ScheduledToolPreparation, error) {
	entry, _ := coordinator.definitions[input.Name]
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
	if evaluation.approval != nil {
		toolCall.state.awaitApproval()
		return ScheduledToolPreparation{
			Stage:     ScheduledApproval,
			Execution: toolCall,
			Approval:  evaluation.approval,
		}, nil
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
func (coordinator *Scheduler) Dispatch(
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
		var unconfirmed *DeliveryError
		if errors.As(err, &unconfirmed) {
			return ScheduledToolDispatch{}, unconfirmed
		}
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
func (coordinator *Scheduler) Finalize(
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
func (coordinator *Scheduler) Finish(
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

func (coordinator *Scheduler) executionState(
	toolCall ToolExecution,
) (*scheduledExecutionState, error) {
	state := toolCall.state
	if state == nil || state.coordinator != coordinator {
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

// ResolveApproval consumes one audited approval result without repeating pre-policy.
func (coordinator *Scheduler) ResolveApproval(toolCall ToolExecution, outcome approval.Outcome) (ScheduledToolPreparation, error) {
	state, err := coordinator.executionState(toolCall)
	if err != nil {
		return ScheduledToolPreparation{}, err
	}
	if err := state.acceptApproval(); err != nil {
		return ScheduledToolPreparation{}, err
	}
	if state.requestContext.Err() != nil {
		return postPreparation(toolCall, abortedResult(false)), nil
	}
	decision, cancelled, err := approvalDecision(toolCall.Name, outcome)
	if err != nil {
		return finalPreparation(toolCall, errorResult(err)), nil
	}
	if cancelled {
		return postPreparation(toolCall, abortedResult(false)), nil
	}
	denial := decisionDenial(toolCall.Name, decision)
	if denial == "" {
		denial, err = coordinator.policies.guardDenial(toolCall)
		if err != nil {
			return finalPreparation(toolCall, errorResult(err)), nil
		}
	}
	if denial != "" {
		return postPreparation(toolCall, deniedResult(denial)), nil
	}
	if state.requestContext.Err() != nil {
		return postPreparation(toolCall, abortedResult(false)), nil
	}
	state.prepareForDispatch()
	return ScheduledToolPreparation{
		Stage:     ScheduledDispatch,
		Execution: toolCall,
	}, nil
}

// PrepareCall captures policy and prepares one immutable invocation.
func (coordinator *Scheduler) PrepareCall(ctx context.Context, input ToolExecutionInput) (*Call, error) {
	prepared, err := coordinator.Prepare(ctx, input)
	if err != nil {
		return nil, err
	}
	return &Call{
		scheduler:   coordinator,
		preparation: prepared,
	}, nil
}
