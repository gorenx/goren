package execution

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/gorenx/goren/internal/jsonvalue"
	"github.com/gorenx/goren/tools"

	"github.com/gorenx/goren/approval"
)

// Scheduler shares compiled tool contracts and invocation capabilities across calls.
// Each prepared Call captures its own policy selection and execution state.
type Scheduler struct {
	definitions map[string]*tools.Contract
	bodies      Invoker
}

// Invoker calls an already-selected body without owning policy or result handling.
// A returned error means body exit is unconfirmed; never fabricate a Tool result.
type Invoker interface {
	Invoke(context.Context, tools.Invocation) (tools.BodyResult, error)
}

// Policies fixes the business collaborators used by prepared executions.
// Borrowed implementations must remain valid until those executions finish.
type Policies struct {
	Before    tools.PrePolicy
	After     tools.PostPolicy
	Execution tools.ExecuteMiddleware
	Results   tools.ResultObserver
	Guards    []tools.ToolGuard
}

// NewScheduler captures executable descriptions independently of registration and Actors.
func NewScheduler(descriptions []tools.Descriptor, bodies Invoker) (*Scheduler, error) {
	if bodies == nil {
		return nil, errors.New("tools: body invoker is required")
	}
	entries := make(map[string]*tools.Contract, len(descriptions))
	for _, description := range descriptions {
		entry, err := tools.CompileDescriptor(description)
		if err != nil {
			return nil, err
		}
		_, found := entries[description.Name]
		if found {
			return nil, errors.New("tools: duplicate execution descriptor")
		}
		entries[description.Name] = entry
	}
	return &Scheduler{
		definitions: entries,
		bodies:      bodies,
	}, nil
}

// prepare validates the call and runs ordered pre-dispatch policy without
// starting the Tool body.
func (coordinator *Scheduler) prepare(
	requestContext context.Context,
	input tools.ToolExecutionInput,
	policies Policies,
) (preparation, error) {
	for _, guard := range policies.Guards {
		if guard == nil {
			return preparation{}, errors.New("tools: guard is required")
		}
	}
	entry, _ := coordinator.definitions[input.Name]
	var finalizer tools.ContentFinalizer
	if entry != nil {
		finalizer = entry.Descriptor().FinalizeContent
	}
	toolCall, err := createExecution(input)
	if requestContext == nil {
		requestContext = context.Background()
		toolCall.state = newScheduledExecutionState(
			coordinator,
			requestContext,
			entry,
			finalizer,
			policies,
		)
		return preparation{
			Stage:     tools.ScheduledFinalResult,
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
		policies,
	)
	if err != nil {
		return finalPreparation(toolCall, errorResult(err)), nil
	}
	if requestContext.Err() != nil {
		return finalPreparation(toolCall, abortedResult(false)), nil
	}
	evaluation, err := toolCall.state.policies.evaluate(
		requestContext,
		toolCall,
	)
	if err != nil {
		return finalPreparation(toolCall, errorResult(err)), nil
	}
	if evaluation.approval != nil {
		toolCall.state.awaitApproval()
		return preparation{
			Stage:     tools.ScheduledApproval,
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
	return preparation{
		Stage:     tools.ScheduledDispatch,
		Execution: toolCall,
	}, nil
}

func (coordinator *Scheduler) executionState(
	toolCall callExecution,
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
	toolCall callExecution,
	outcome tools.ToolExecutionResult,
) preparation {
	toolCall.state.prepareForFinish()
	return preparation{
		Stage:     tools.ScheduledFinalResult,
		Execution: toolCall,
		Result:    outcome,
	}
}

func postPreparation(
	toolCall callExecution,
	outcome tools.ToolExecutionResult,
) preparation {
	toolCall.state.prepareForPost()
	return preparation{
		Stage:     tools.ScheduledPostResult,
		Execution: toolCall,
		Result:    outcome,
	}
}

// applyApproval consumes one audited approval result without repeating pre-policy.
func (coordinator *Scheduler) applyApproval(toolCall callExecution, outcome approval.Outcome) (preparation, error) {
	state, err := coordinator.executionState(toolCall)
	if err != nil {
		return preparation{}, err
	}
	err = state.acceptApproval()
	if err != nil {
		return preparation{}, err
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
		denial, err = state.policies.guardDenial(toolCall)
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
	return preparation{
		Stage:     tools.ScheduledDispatch,
		Execution: toolCall,
	}, nil
}

// PrepareCall captures policy and settles any named approval before admitting dispatch.
// An unconfirmed approval returns the retained Call with its delivery error.
func (coordinator *Scheduler) PrepareCall(ctx context.Context, input tools.ToolExecutionInput, policies Policies) (*Call, error) {
	prepared, err := coordinator.prepare(ctx, input, policies)
	if err != nil {
		return nil, err
	}
	current := &Call{
		scheduler:   coordinator,
		preparation: prepared,
	}
	if prepared.Stage == tools.ScheduledApproval {
		current.preparation, err = coordinator.requestApproval(prepared)
		if err != nil {
			current.failure = err
		}
	}
	return current, err
}

// ExecutionMode classifies overlap using the captured definition and validated input.
func (coordinator *Scheduler) ExecutionMode(input tools.ToolExecutionInput) tools.ToolExecutionMode {
	arguments, err := jsonvalue.Clone(input.Arguments)
	if err != nil {
		return tools.ExecutionExclusive
	}
	entry, found := coordinator.definitions[input.Name]
	if !found || entry.Descriptor().ConcurrencyBehavior == nil {
		return tools.ExecutionExclusive
	}
	err = entry.ValidateArguments(arguments)
	if err != nil {
		return tools.ExecutionExclusive
	}
	if concurrencySafe(entry.Descriptor().ConcurrencyBehavior, arguments) {
		return tools.ExecutionParallel
	}
	return tools.ExecutionExclusive
}

func concurrencySafe(
	classifier tools.ConcurrencyClassifier,
	arguments json.RawMessage,
) (safe bool) {
	defer func() {
		if recover() != nil {
			safe = false
		}
	}()
	return classifier.ConcurrencySafe(
		append(json.RawMessage(nil), arguments...),
	)
}
