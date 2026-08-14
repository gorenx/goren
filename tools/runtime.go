package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/gorenx/goren/internal/jsonvalue"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
)

func (owner *toolRegistry) Execute(requestContext context.Context, input ToolExecutionInput) ToolExecutionResult {
	entry := owner.store.view(input.Scope).visible[input.Name]
	var finalizer ContentFinalizer
	if entry != nil {
		finalizer = entry.definition.FinalizeContent
	}
	execution, err := createExecution(input)
	if err != nil {
		if requestContext == nil {
			requestContext = context.Background()
		}
		return owner.finish(requestContext, execution, errorResult(err), finalizer)
	}
	bodyInvoked := false
	if requestContext == nil {
		return owner.finish(context.Background(), execution, errorResult(errors.New("tools: execution context is nil")), finalizer)
	}
	if requestContext.Err() != nil {
		return owner.finish(requestContext, execution, abortedResult(false), finalizer)
	}
	gate, err := plugin.WaterfallScopedFrom(
		requestContext, owner.sourceScope, execution.Scope, preExecuteEvent, execution,
		func(context.Context, ToolExecution) (PreToolDecision, error) { return AllowDecision{}, nil },
	)
	if err != nil {
		return owner.finish(requestContext, execution, errorResult(err), finalizer)
	}
	denial := decisionDenial(execution.Name, gate)
	if denial == "" {
		denial, err = owner.guardDenial(execution)
		if err != nil {
			return owner.finish(requestContext, execution, errorResult(err), finalizer)
		}
	}
	var outcome ToolExecutionResult
	if denial != "" {
		outcome = deniedResult(denial)
	} else if requestContext.Err() != nil {
		outcome = abortedResult(false)
	} else {
		outcome, err = owner.dispatch(requestContext, execution, entry, &bodyInvoked)
		if err != nil {
			return owner.finish(requestContext, execution, errorResult(err), finalizer)
		}
		if requestContext.Err() != nil && !outcome.Failed() {
			outcome = abortedResult(bodyInvoked)
		}
	}
	outcome, err = owner.post(requestContext, execution, entry, outcome)
	if err != nil {
		return owner.finish(requestContext, execution, errorResult(err), finalizer)
	}
	if requestContext.Err() != nil && !outcome.Failed() {
		outcome = abortedResult(bodyInvoked)
	}
	return owner.finish(requestContext, execution, outcome, finalizer)
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
	bodyInvoked *bool,
) (ToolExecutionResult, error) {
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
			*bodyInvoked = true
			var concludesTurn atomic.Bool
			value, err := invokeExecutor(entry.definition.Executor, execution.arguments, ToolRunContext{
				Context: fusedContext, Execution: execution,
				conclude: func() { concludesTurn.Store(true) },
			})
			if err != nil {
				return errorResult(err), nil
			}
			outcome, err := owner.success(execution, entry, value)
			if err != nil {
				return errorResult(err), nil
			}
			if fusedContext.Err() != nil {
				return abortedResult(true), nil
			}
			if succeeded, ok := outcome.(*ToolExecutionSuccess); ok && concludesTurn.Load() {
				succeeded.ConcludesTurn = true
			}
			return outcome, nil
		},
	)
	if err != nil {
		return nil, err
	}
	return owner.normalizeDispatch(execution, entry, candidate)
}

func (owner *toolRegistry) post(
	requestContext context.Context,
	execution ToolExecution,
	entry *registeredTool,
	outcome ToolExecutionResult,
) (ToolExecutionResult, error) {
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
		return detached, nil
	case ReplaceContentDecision:
		content, cloneErr := llm.CloneContentBlocks(selected.Content)
		if cloneErr != nil {
			return nil, cloneErr
		}
		return replaceResultContent(detached, content), nil
	case ReplaceValueDecision:
		if detached.Failed() {
			return nil, errors.New("tools: post-execute cannot replace the value of a failed result")
		}
		if entry == nil {
			return nil, &ToolNotFoundError{Name: execution.Name}
		}
		return owner.success(execution, entry, selected.Value)
	case BlockDecision:
		content, cloneErr := llm.CloneContentBlocks(selected.Feedback)
		if cloneErr != nil {
			return nil, cloneErr
		}
		return &ToolExecutionFailure{
			Error: ToolFailure{Message: failureMessage(content)}, Content: content,
		}, nil
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
		return owner.success(execution, entry, succeeded.Value)
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
		Scope: input.Scope, Parent: input.Parent, Token: ToolExecutionToken{token: identity},
	}
	if strings.TrimSpace(input.Name) == "" || input.Name != strings.TrimSpace(input.Name) {
		return execution, errors.New("tools: tool name must be non-empty and trimmed")
	}
	if input.CallID == "" {
		return execution, errors.New("tools: call ID is empty")
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

func decisionDenial(name string, decision PreToolDecision) string {
	switch selected := decision.(type) {
	case AllowDecision:
		return ""
	case DenyDecision:
		return selected.Reason
	case AskDecision:
		if strings.TrimSpace(selected.Reason) != "" {
			return selected.Reason
		}
		return fmt.Sprintf("tool %q requires approval (not yet supported)", name)
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
