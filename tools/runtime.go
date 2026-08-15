package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/gorenx/goren/internal/jsonvalue"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
)

func (owner *toolRegistry) Execute(requestContext context.Context, input ToolExecutionInput) ToolExecutionResult {
	preparation := owner.Prepare(requestContext, input)
	switch preparation.Stage {
	case ScheduledDispatch:
		dispatched := owner.Dispatch(preparation.Execution)
		if dispatched.NeedsPost {
			return owner.Finalize(preparation.Execution, dispatched.Result)
		}
		return owner.Finish(preparation.Execution, dispatched.Result)
	case ScheduledPostResult:
		return owner.Finalize(preparation.Execution, preparation.Result)
	case ScheduledFinalResult:
		return owner.Finish(preparation.Execution, preparation.Result)
	default:
		return errorResult(fmt.Errorf("tools: unsupported scheduled stage %q", preparation.Stage))
	}
}

// Scheduler returns the registry-owned staged execution capability used by Agent Loop.
func (owner *toolRegistry) Scheduler() ToolExecutionScheduler { return owner }

// Prepare runs ordered pre-execute policy and guards without starting the body.
func (owner *toolRegistry) Prepare(requestContext context.Context, input ToolExecutionInput) ScheduledToolPreparation {
	entry := owner.store.view(input.Scope).visible[input.Name]
	var finalizer ContentFinalizer
	if entry != nil {
		finalizer = entry.definition.FinalizeContent
	}
	execution, err := createExecution(input)
	if requestContext == nil {
		requestContext = context.Background()
		execution.state = &scheduledExecutionState{
			owner: owner, requestContext: requestContext, entry: entry, finalizer: finalizer,
		}
		return ScheduledToolPreparation{
			Stage: ScheduledFinalResult, Execution: execution,
			Result: errorResult(errors.New("tools: execution context is nil")),
		}
	}
	execution.state = &scheduledExecutionState{
		owner: owner, requestContext: requestContext, entry: entry, finalizer: finalizer,
	}
	if err != nil {
		return ScheduledToolPreparation{Stage: ScheduledFinalResult, Execution: execution, Result: errorResult(err)}
	}
	if requestContext.Err() != nil {
		return ScheduledToolPreparation{Stage: ScheduledFinalResult, Execution: execution, Result: abortedResult(false)}
	}
	gate, err := plugin.WaterfallScopedFrom(
		requestContext, owner.sourceScope, execution.Scope, preExecuteEvent, execution,
		func(context.Context, ToolExecution) (PreToolDecision, error) { return AllowDecision{}, nil },
	)
	if err != nil {
		return ScheduledToolPreparation{Stage: ScheduledFinalResult, Execution: execution, Result: errorResult(err)}
	}
	resolvedGate, approvalCancelled, err := owner.resolveAsk(requestContext, execution, gate)
	if err != nil {
		return ScheduledToolPreparation{Stage: ScheduledFinalResult, Execution: execution, Result: errorResult(err)}
	}
	if approvalCancelled && requestContext.Err() != nil {
		return ScheduledToolPreparation{Stage: ScheduledPostResult, Execution: execution, Result: abortedResult(false)}
	}
	denial := decisionDenial(execution.Name, resolvedGate)
	if denial == "" {
		denial, err = owner.guardDenial(execution)
		if err != nil {
			return ScheduledToolPreparation{Stage: ScheduledFinalResult, Execution: execution, Result: errorResult(err)}
		}
	}
	if denial != "" {
		return ScheduledToolPreparation{Stage: ScheduledPostResult, Execution: execution, Result: deniedResult(denial)}
	}
	if requestContext.Err() != nil {
		return ScheduledToolPreparation{Stage: ScheduledPostResult, Execution: execution, Result: abortedResult(false)}
	}
	return ScheduledToolPreparation{Stage: ScheduledDispatch, Execution: execution}
}

// Dispatch runs only the around-dispatch chain and body; Agent Loop may overlap this stage.
func (owner *toolRegistry) Dispatch(execution ToolExecution) ScheduledToolDispatch {
	state, err := owner.executionState(execution)
	if err != nil {
		return ScheduledToolDispatch{Result: errorResult(err)}
	}
	outcome, err := owner.dispatch(state.requestContext, execution, state.entry)
	if err != nil {
		return ScheduledToolDispatch{Result: errorResult(err)}
	}
	return ScheduledToolDispatch{NeedsPost: true, Result: outcome}
}

// Finalize applies ordered post-execute policy, cancellation, final content, and observation.
func (owner *toolRegistry) Finalize(execution ToolExecution, outcome ToolExecutionResult) ToolExecutionResult {
	state, err := owner.executionState(execution)
	if err != nil {
		return errorResult(err)
	}
	postOutcome, err := owner.post(state.requestContext, execution, state.entry, outcome)
	if err != nil {
		return owner.finish(state.requestContext, execution, errorResult(err), state.finalizer)
	}
	bodyInvoked, _, _ := state.executionOutcome()
	if state.requestContext.Err() != nil && !postOutcome.Failed() {
		postOutcome = abortedResult(bodyInvoked, postOutcome)
	}
	return owner.finish(state.requestContext, execution, postOutcome, state.finalizer)
}

// Finish materializes a terminal staged result without running post-execute policy.
func (owner *toolRegistry) Finish(execution ToolExecution, outcome ToolExecutionResult) ToolExecutionResult {
	state, err := owner.executionState(execution)
	if err != nil {
		return errorResult(err)
	}
	return owner.finish(state.requestContext, execution, outcome, state.finalizer)
}

func (owner *toolRegistry) guardDenial(execution ToolExecution) (string, error) {
	for _, policy := range owner.store.guards(execution.Scope) {
		reason, denied, err := invokeGuard(policy, execution)
		if err != nil {
			return "", err
		}
		if denied {
			if strings.TrimSpace(reason) == "" {
				return "tool execution denied by guard", nil
			}
			return reason, nil
		}
	}
	return "", nil
}

func (owner *toolRegistry) dispatch(
	requestContext context.Context,
	execution ToolExecution,
	entry *registeredTool,
) (ToolExecutionResult, error) {
	state, err := owner.executionState(execution)
	if err != nil {
		return nil, err
	}
	candidate, err := plugin.WaterfallScopedFrom(
		requestContext, owner.sourceScope, execution.Scope, executeEvent, execution,
		func(chainContext context.Context, _ ToolExecution) (ToolExecutionResult, error) {
			if chainContext == nil {
				return nil, errors.New("tools: execute wrapper delegated with a nil context")
			}
			fusedContext, releaseFusion := fuseCancellation(requestContext, chainContext)
			defer releaseFusion()
			if fusedContext.Err() != nil {
				return abortedResult(false), nil
			}
			if entry == nil {
				return errorResult(&ToolNotFoundError{Name: execution.Name}), nil
			}
			if err := validateSchemaValue(entry.parameterSchema, execution.arguments, "arguments"); err != nil {
				return errorResult(&ToolArgumentsError{Violations: []string{err.Error()}}), nil
			}
			state.markBodyInvoked()
			value, err := invokeExecutor(entry.definition.Executor, execution.arguments, ToolRunContext{
				Context: fusedContext, Execution: execution,
			})
			if err != nil {
				return errorResult(err), nil
			}
			outcome, err := owner.success(execution, entry, value)
			if err != nil {
				return errorResult(err), nil
			}
			_, concludesTurn, _ := state.executionOutcome()
			if succeeded, ok := outcome.(*ToolExecutionSuccess); ok && concludesTurn {
				succeeded.ConcludesTurn = true
			}
			return outcome, nil
		},
	)
	if err != nil {
		return nil, err
	}
	normalized, err := owner.normalizeDispatch(execution, entry, candidate)
	if err != nil {
		return nil, err
	}
	bodyInvoked, _, deferredContexts := state.executionOutcome()
	normalized, err = appendAdditionalContexts(normalized, deferredContexts)
	if err != nil {
		return nil, err
	}
	if requestContext.Err() != nil && !normalized.Failed() {
		return abortedResult(bodyInvoked, normalized), nil
	}
	return normalized, nil
}

func (owner *toolRegistry) post(
	requestContext context.Context,
	execution ToolExecution,
	entry *registeredTool,
	outcome ToolExecutionResult,
) (ToolExecutionResult, error) {
	if outcome == nil {
		return nil, errors.New("tools: staged result is nil")
	}
	detached, err := outcome.cloneResult()
	if err != nil {
		return nil, err
	}
	snapshot, err := newResultSnapshot(detached)
	if err != nil {
		return nil, err
	}
	decision, err := plugin.WaterfallScopedFrom(
		requestContext, owner.sourceScope, execution.Scope, postExecuteEvent,
		postExecutePayload{execution: execution, snapshot: snapshot},
		func(context.Context, postExecutePayload) (PostToolDecision, error) { return AcceptDecision{}, nil },
	)
	if err != nil {
		return nil, err
	}
	switch selected := decision.(type) {
	case AcceptDecision:
		return appendAdditionalContexts(detached, selected.AdditionalContexts)
	case ReplaceContentDecision:
		content, cloneErr := llm.CloneContentBlocks(selected.Content)
		if cloneErr != nil {
			return nil, cloneErr
		}
		return appendAdditionalContexts(replaceResultContent(detached, content), selected.AdditionalContexts)
	case ReplaceValueDecision:
		if detached.Failed() {
			return nil, errors.New("tools: post-execute cannot replace the value of a failed result")
		}
		if entry == nil {
			return nil, &ToolNotFoundError{Name: execution.Name}
		}
		replaced, replaceErr := owner.success(execution, entry, selected.Value)
		if replaceErr != nil {
			return nil, replaceErr
		}
		combined := append(detached.AdditionalContextMessages(), selected.AdditionalContexts...)
		return appendAdditionalContexts(replaced, combined)
	case BlockDecision:
		content, cloneErr := llm.CloneContentBlocks(selected.Feedback)
		if cloneErr != nil {
			return nil, cloneErr
		}
		blocked := &ToolExecutionFailure{
			Error: ToolFailure{Message: failureMessage(content)}, Content: content,
		}
		return appendAdditionalContexts(blocked, selected.AdditionalContexts)
	case nil:
		return nil, errors.New("tools: post-execute returned a nil decision")
	default:
		return nil, fmt.Errorf("tools: unsupported post-execute decision %T", decision)
	}
}

func (owner *toolRegistry) normalizeDispatch(
	execution ToolExecution,
	entry *registeredTool,
	candidate ToolExecutionResult,
) (ToolExecutionResult, error) {
	if candidate == nil {
		return nil, errors.New("tools: execute handler returned a nil result")
	}
	if succeeded, ok := candidate.(*ToolExecutionSuccess); ok {
		if succeeded.owner == execution.Token.token {
			return succeeded.cloneResult()
		}
		if entry == nil {
			return nil, &ToolNotFoundError{Name: execution.Name}
		}
		normalized, err := owner.success(execution, entry, succeeded.Value)
		if err != nil {
			return nil, err
		}
		return appendAdditionalContexts(normalized, succeeded.AdditionalContexts)
	}
	if failureOutcome, ok := candidate.(*ToolExecutionFailure); ok {
		return failureOutcome.cloneResult()
	}
	return nil, fmt.Errorf("tools: unsupported execution result %T", candidate)
}

func (owner *toolRegistry) success(
	execution ToolExecution,
	entry *registeredTool,
	candidate json.RawMessage,
) (ToolExecutionResult, error) {
	value, err := jsonvalue.Clone(candidate)
	if err != nil {
		return nil, &ToolOutputError{Name: execution.Name, Violations: []string{"value is not lossless JSON: " + err.Error()}}
	}
	if err := validateSchemaValue(entry.outputSchema, value, "value"); err != nil {
		return nil, &ToolOutputError{Name: execution.Name, Violations: []string{err.Error()}}
	}
	content, err := invokeRenderer(entry.definition.Output.Renderer, execution.arguments, value)
	if err != nil {
		return nil, &ToolOutputError{Name: execution.Name, Violations: []string{"output.render failed: " + err.Error()}}
	}
	content, err = llm.CloneContentBlocks(content)
	if err != nil {
		return nil, &ToolOutputError{Name: execution.Name, Violations: []string{"output.render returned invalid content: " + err.Error()}}
	}
	var meta json.RawMessage
	if execution.Parent.IsZero() && entry.definition.Output.PresentationMeta != nil {
		meta, err = invokeProjector(entry.definition.Output.PresentationMeta, execution.arguments, value)
		if err == nil {
			meta, err = jsonvalue.Clone(meta)
		}
		if err != nil {
			return nil, &ToolOutputError{Name: execution.Name, Violations: []string{"output.presentationMeta failed: " + err.Error()}}
		}
	}
	return &ToolExecutionSuccess{
		Value: value, Content: content, Meta: meta, owner: execution.Token.token,
	}, nil
}

func (owner *toolRegistry) finish(
	requestContext context.Context,
	execution ToolExecution,
	outcome ToolExecutionResult,
	finalizer ContentFinalizer,
) ToolExecutionResult {
	finalOutcome, err := outcome.cloneResult()
	if err != nil {
		finalOutcome = errorResult(err)
	}
	if finalizer != nil {
		content, replace, finalizeErr := invokeFinalizer(finalizer, execution, finalOutcome)
		if finalizeErr != nil {
			finalOutcome = errorResult(finalizeErr)
		} else if replace {
			content, finalizeErr = llm.CloneContentBlocks(content)
			if finalizeErr != nil {
				finalOutcome = errorResult(finalizeErr)
			} else {
				finalOutcome = replaceResultContent(finalOutcome, content)
			}
		}
	}
	materialized, err := finalOutcome.cloneResult()
	if err != nil {
		materialized = errorResult(err)
	}
	snapshot, err := newResultSnapshot(materialized)
	if err != nil {
		materialized = errorResult(err)
		snapshot, _ = newResultSnapshot(materialized)
	}
	observerErr := plugin.EmitScopedFrom(
		requestContext, owner.sourceScope, execution.Scope, resultEvent,
		resultPayload{execution: cloneExecution(execution), snapshot: snapshot},
	)
	if observerErr != nil {
		owner.reportObserverFailure(requestContext, execution, observerErr)
	}
	returned, err := materialized.cloneResult()
	if err != nil {
		return errorResult(err)
	}
	return returned
}

func (owner *toolRegistry) reportObserverFailure(requestContext context.Context, execution ToolExecution, observerErr error) {
	if owner.reporter == nil {
		return
	}
	defer func() { _ = recover() }()
	owner.reporter.ReportToolObserverError(requestContext, cloneExecution(execution), observerErr)
}

func createExecution(input ToolExecutionInput) (ToolExecution, error) {
	identity := &toolExecutionToken{}
	rootCallID := input.RootCallID
	if rootCallID == "" {
		rootCallID = input.CallID
	}
	execution := ToolExecution{
		CallID: input.CallID, RootCallID: rootCallID, Name: input.Name,
		Scope: input.Scope, Subject: input.Subject,
		Parent: input.Parent, Token: ToolExecutionToken{token: identity},
	}
	if strings.TrimSpace(input.Name) == "" || input.Name != strings.TrimSpace(input.Name) {
		return execution, errors.New("tools: tool name must be non-empty and trimmed")
	}
	if input.CallID == "" {
		return execution, errors.New("tools: call ID is empty")
	}
	if input.Subject != nil {
		if input.Subject.ScopeValue() == nil || input.Subject.SessionValue() == nil {
			return execution, errors.New("tools: execution Agent must expose a Scope and Session")
		}
		if input.Subject.ScopeValue().Target() != input.Scope {
			return execution, errors.New("tools: execution Scope does not match its Agent")
		}
	}
	arguments, err := jsonvalue.Clone(input.Arguments)
	if err != nil {
		return execution, fmt.Errorf("tools: arguments are not lossless JSON: %w", err)
	}
	execution.arguments = arguments
	return execution, nil
}

func cloneExecution(source ToolExecution) ToolExecution {
	source.arguments = append(json.RawMessage(nil), source.arguments...)
	return source
}

func (owner *toolRegistry) executionState(execution ToolExecution) (*scheduledExecutionState, error) {
	state := execution.state
	if state == nil || state.owner != owner || execution.Token.token == nil {
		return nil, errors.New("tools: execution was not prepared by this registry")
	}
	return state, nil
}

func decisionDenial(name string, decision PreToolDecision) string {
	switch selected := decision.(type) {
	case AllowDecision:
		return ""
	case DenyDecision:
		return selected.Reason
	case AskDecision:
		return fmt.Sprintf("tool %q approval decision was not resolved", name)
	case nil:
		return "pre-execute policy returned no decision"
	default:
		return fmt.Sprintf("unsupported pre-execute decision %T", decision)
	}
}

func deniedResult(reason string) ToolExecutionResult {
	return &ToolExecutionFailure{
		Error:   ToolFailure{Message: reason},
		Content: []llm.ContentBlock{llm.NewTextBlock("Error: " + reason)},
	}
}

func fuseCancellation(callerContext context.Context, wrapperContext context.Context) (context.Context, func()) {
	fusedContext, cancel := context.WithCancelCause(wrapperContext)
	stop := context.AfterFunc(callerContext, func() {
		cancel(cancellationCause(callerContext))
	})
	if callerContext.Err() != nil {
		cancel(cancellationCause(callerContext))
	}
	return fusedContext, func() {
		stop()
		cancel(nil)
	}
}

func cancellationCause(requestContext context.Context) error {
	if cause := context.Cause(requestContext); cause != nil {
		return cause
	}
	return requestContext.Err()
}

func concurrencySafe(classifier ConcurrencyClassifier, arguments json.RawMessage) (safe bool) {
	defer func() {
		if recover() != nil {
			safe = false
		}
	}()
	return classifier.ConcurrencySafe(append(json.RawMessage(nil), arguments...))
}

func invokeGuard(policy ToolGuard, execution ToolExecution) (reason string, denied bool, invokeErr error) {
	defer func() {
		if panicValue := recover(); panicValue != nil {
			invokeErr = fmt.Errorf("tools: guard panicked: %v", panicValue)
		}
	}()
	reason, denied = policy.DenyReason(cloneExecution(execution))
	return reason, denied, nil
}

func invokeExecutor(body Executor, arguments json.RawMessage, runContext ToolRunContext) (value json.RawMessage, invokeErr error) {
	defer func() {
		if panicValue := recover(); panicValue != nil {
			invokeErr = fmt.Errorf("tools: executor panicked: %v", panicValue)
		}
	}()
	return body.Execute(append(json.RawMessage(nil), arguments...), runContext)
}

func invokeRenderer(renderer OutputRenderer, arguments json.RawMessage, value json.RawMessage) (content []llm.ContentBlock, invokeErr error) {
	defer func() {
		if panicValue := recover(); panicValue != nil {
			invokeErr = fmt.Errorf("%v", panicValue)
		}
	}()
	return renderer.Render(
		append(json.RawMessage(nil), arguments...), append(json.RawMessage(nil), value...),
	)
}

func invokeProjector(projector PresentationProjector, arguments json.RawMessage, value json.RawMessage) (meta json.RawMessage, invokeErr error) {
	defer func() {
		if panicValue := recover(); panicValue != nil {
			invokeErr = fmt.Errorf("%v", panicValue)
		}
	}()
	return projector.Project(
		append(json.RawMessage(nil), arguments...), append(json.RawMessage(nil), value...),
	)
}

func invokeFinalizer(finalizer ContentFinalizer, execution ToolExecution, outcome ToolExecutionResult) (content []llm.ContentBlock, replace bool, invokeErr error) {
	defer func() {
		if panicValue := recover(); panicValue != nil {
			invokeErr = fmt.Errorf("tools: content finalizer panicked: %v", panicValue)
		}
	}()
	snapshot, err := newResultSnapshot(outcome)
	if err != nil {
		return nil, false, err
	}
	content, replace = finalizer.Finalize(cloneExecution(execution), snapshot)
	return content, replace, nil
}
