package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/internal/jsonvalue"
	"github.com/gorenx/goren/tools"
)

// DeliveryError denotes an unconfirmed tool invocation, not a body failure.
// The caller must retain execution responsibility instead of committing a result.
type DeliveryError struct {
	Cause error
}

func (failure *DeliveryError) Error() string {
	return "tools: invocation completion unconfirmed: " + failure.Cause.Error()
}
func (failure *DeliveryError) Unwrap() error { return failure.Cause }

func (current *Call) invoke(
	requestContext context.Context,
	toolCall callExecution,
	entry *tools.Contract,
) (tools.ToolExecutionResult, error) {
	state := toolCall.state
	if state == nil {
		return nil, errors.New("tools: execution state is unavailable")
	}
	dispatchOutcome, err := state.pipeline.ExecuteBody(
		requestContext,
		tools.NewExecuteRequest(cloneExecution(toolCall)),
		executeTerminal{
			call:          current,
			callerContext: requestContext,
			toolCall:      toolCall,
			entry:         entry,
		},
	)
	if err != nil {
		return nil, err
	}
	normalized, err := current.normalize(
		toolCall,
		entry,
		dispatchOutcome.Result,
	)
	if err != nil {
		return nil, err
	}
	stateSnapshot := state.snapshot()
	normalized, err = prependAdditionalContexts(
		normalized,
		stateSnapshot.deferredContexts,
	)
	if err != nil {
		return nil, err
	}
	if requestContext.Err() != nil && !normalized.Failed() {
		return abortedResult(stateSnapshot.bodyInvoked, normalized), nil
	}
	return normalized, nil
}

type executeTerminal struct {
	call          *Call
	callerContext context.Context
	toolCall      callExecution
	entry         *tools.Contract
}

func (terminal executeTerminal) Execute(
	chainContext context.Context,
	_ tools.ExecuteRequest,
) (tools.ExecuteOutcome, error) {
	if chainContext == nil {
		return tools.ExecuteOutcome{}, errors.New(
			"tools: execute Middleware delegated with a nil Context",
		)
	}
	outcome, err := terminal.call.executeBody(
		terminal.callerContext,
		chainContext,
		terminal.toolCall,
		terminal.entry,
	)
	return tools.ExecuteOutcome{
		Result: outcome,
	}, err
}

func (current *Call) executeBody(
	requestContext context.Context,
	chainContext context.Context,
	toolCall callExecution,
	entry *tools.Contract,
) (tools.ToolExecutionResult, error) {
	state := toolCall.state
	if state == nil {
		return nil, errors.New("tools: execution state is unavailable")
	}
	fusedContext, releaseFusion := fuseCancellation(
		requestContext,
		chainContext,
	)
	defer releaseFusion()
	if fusedContext.Err() != nil {
		return abortedResult(false), nil
	}
	if entry == nil {
		return errorResult(&tools.ToolNotFoundError{
			Name: toolCall.Name,
		}), nil
	}
	err := entry.ValidateArguments(toolCall.ArgumentsJSON())
	if err != nil {
		return errorResult(&tools.ToolArgumentsError{
			Violations: []string{
				err.Error(),
			},
		}), nil
	}
	state.markBodyInvoked()
	body, err := invokeBody(current.scheduler.bodies, fusedContext, toolCall.Invocation())
	if err != nil {
		return nil, &DeliveryError{
			Cause: err,
		}
	}
	state.recordEffects(body)
	if body.Err != nil {
		return errorResult(body.Err), nil
	}
	outcome, err := current.success(toolCall, entry, body.Value)
	if err != nil {
		return errorResult(err), nil
	}
	return &canonicalSuccess{
		execution:            toolCall.state,
		ToolExecutionSuccess: outcome.(*tools.ToolExecutionSuccess),
	}, nil
}

func (current *Call) normalize(
	toolCall callExecution,
	entry *tools.Contract,
	candidate tools.ToolExecutionResult,
) (tools.ToolExecutionResult, error) {
	if candidate == nil {
		return nil, errors.New("tools: execute handler returned a nil result")
	}
	canonical, canonicalMatches := candidate.(*canonicalSuccess)
	if canonicalMatches {
		if canonical.execution == toolCall.state {
			return tools.CloneResult(canonical)
		}
		return current.normalizeAuthoredSuccess(
			toolCall,
			entry,
			canonical.ToolExecutionSuccess,
		)
	}
	succeeded, succeededMatches := candidate.(*tools.ToolExecutionSuccess)
	if succeededMatches {
		return current.normalizeAuthoredSuccess(
			toolCall,
			entry,
			succeeded,
		)
	}
	failedOutcome, failedOutcomeMatches := candidate.(*tools.ToolExecutionFailure)
	if failedOutcomeMatches {
		return tools.CloneResult(failedOutcome)
	}
	return nil, fmt.Errorf(
		"tools: unsupported execution result %T",
		candidate,
	)
}

func (current *Call) normalizeAuthoredSuccess(
	toolCall callExecution,
	entry *tools.Contract,
	authored *tools.ToolExecutionSuccess,
) (tools.ToolExecutionResult, error) {
	if authored == nil {
		return nil, errors.New("tools: execute handler returned a nil success")
	}
	if entry == nil {
		return nil, &tools.ToolNotFoundError{
			Name: toolCall.Name,
		}
	}
	normalized, err := current.success(
		toolCall,
		entry,
		authored.Value,
	)
	if err != nil {
		return nil, err
	}
	return appendAdditionalContexts(
		normalized,
		authored.AdditionalContexts,
	)
}

func (current *Call) success(
	toolCall callExecution,
	entry *tools.Contract,
	candidate json.RawMessage,
) (tools.ToolExecutionResult, error) {
	value, err := jsonvalue.Clone(candidate)
	if err != nil {
		return nil, outputError(
			toolCall.Name,
			"value is not lossless JSON: "+err.Error(),
		)
	}
	err = entry.ValidateValue(value)
	if err != nil {
		return nil, outputError(toolCall.Name, err.Error())
	}
	content, err := invokeRenderer(
		entry.Descriptor().Output.Renderer,
		toolCall.ArgumentsJSON(),
		value,
	)
	if err != nil {
		return nil, outputError(
			toolCall.Name,
			"output.render failed: "+err.Error(),
		)
	}
	content, err = agentmessage.CloneContentBlocks(content)
	if err != nil {
		return nil, outputError(
			toolCall.Name,
			"output.render returned invalid content: "+err.Error(),
		)
	}
	var meta json.RawMessage
	if toolCall.RootCallID == toolCall.CallID &&
		entry.Descriptor().Output.PresentationMeta != nil {
		meta, err = invokeProjector(
			entry.Descriptor().Output.PresentationMeta,
			toolCall.ArgumentsJSON(),
			value,
		)
		if err == nil {
			meta, err = jsonvalue.Clone(meta)
		}
		if err != nil {
			return nil, outputError(
				toolCall.Name,
				"output.presentationMeta failed: "+err.Error(),
			)
		}
	}
	stateSnapshot := toolCall.state.snapshot()
	return &tools.ToolExecutionSuccess{
		Value:         value,
		Content:       content,
		Meta:          meta,
		ConcludesTurn: stateSnapshot.concludesTurn,
	}, nil
}

func outputError(name string, violation string) *tools.ToolOutputError {
	return &tools.ToolOutputError{
		Name: name,
		Violations: []string{
			violation,
		},
	}
}

func fuseCancellation(
	callerContext context.Context,
	wrapperContext context.Context,
) (context.Context, func()) {
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
	cause := context.Cause(requestContext)
	if cause != nil {
		return cause
	}
	return requestContext.Err()
}

func invokeRenderer(
	renderer tools.OutputRenderer,
	arguments json.RawMessage,
	value json.RawMessage,
) (content []agentmessage.ContentBlock, invokeErr error) {
	defer func() {
		panicValue := recover()
		if panicValue != nil {
			invokeErr = fmt.Errorf("%v", panicValue)
		}
	}()
	return renderer.Render(
		append(json.RawMessage(nil), arguments...),
		append(json.RawMessage(nil), value...),
	)
}

func invokeProjector(
	projector tools.PresentationProjector,
	arguments json.RawMessage,
	value json.RawMessage,
) (meta json.RawMessage, invokeErr error) {
	defer func() {
		panicValue := recover()
		if panicValue != nil {
			invokeErr = fmt.Errorf("%v", panicValue)
		}
	}()
	return projector.Project(
		append(json.RawMessage(nil), arguments...),
		append(json.RawMessage(nil), value...),
	)
}

// invokeBody preserves uncertainty if a delivery adapter panics after submission.
func invokeBody(endpoint Invoker, ctx context.Context, input tools.Invocation) (result tools.BodyResult, failure error) {
	defer func() {
		if recover() != nil {
			result = tools.BodyResult{}
			failure = errors.New("tools: body delivery panicked; execution completion is unconfirmed")
		}
	}()
	return endpoint.Invoke(ctx, input)
}
