package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gorenx/goren/internal/jsonvalue"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
)

// dispatcher owns the around-dispatch Waterfall, Tool body invocation, and
// canonical successful-output normalization.
type dispatcher struct {
	source plugin.Plugin
}

func (bodyDispatcher *dispatcher) dispatch(
	requestContext context.Context,
	toolCall ToolExecution,
	entry *registeredTool,
) (ToolExecutionResult, error) {
	state := toolCall.state
	if state == nil {
		return nil, errors.New("tools: execution state is unavailable")
	}
	dispatchOutcome, err := plugin.Run(
		requestContext,
		bodyDispatcher.source,
		ExecuteRequest{
			toolCall: toolCall,
		},
		executeTerminal{
			dispatcher:    bodyDispatcher,
			callerContext: requestContext,
			toolCall:      toolCall,
			entry:         entry,
		},
	)
	if err != nil {
		return nil, err
	}
	normalized, err := bodyDispatcher.normalize(
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
	dispatcher    *dispatcher
	callerContext context.Context
	toolCall      ToolExecution
	entry         *registeredTool
}

func (terminal executeTerminal) Execute(
	chainContext context.Context,
	_ ExecuteRequest,
) (ExecuteOutcome, error) {
	if chainContext == nil {
		return ExecuteOutcome{}, errors.New(
			"tools: execute Middleware delegated with a nil Context",
		)
	}
	outcome, err := terminal.dispatcher.executeBody(
		terminal.callerContext,
		chainContext,
		terminal.toolCall,
		terminal.entry,
	)
	return ExecuteOutcome{
		Result: outcome,
	}, err
}

func (bodyDispatcher *dispatcher) executeBody(
	requestContext context.Context,
	chainContext context.Context,
	toolCall ToolExecution,
	entry *registeredTool,
) (ToolExecutionResult, error) {
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
		return errorResult(&ToolNotFoundError{
			Name: toolCall.Name,
		}), nil
	}
	if err := validateSchemaValue(
		entry.parameterSchema,
		toolCall.arguments,
		"arguments",
	); err != nil {
		return errorResult(&ToolArgumentsError{
			Violations: []string{
				err.Error(),
			},
		}), nil
	}
	state.markBodyInvoked()
	value, err := invokeExecutor(
		entry.definition.Executor,
		toolCall.arguments,
		ToolRunContext{
			Context:   fusedContext,
			Execution: toolCall,
		},
	)
	if err != nil {
		return errorResult(err), nil
	}
	outcome, err := bodyDispatcher.success(toolCall, entry, value)
	if err != nil {
		return errorResult(err), nil
	}
	return &canonicalToolExecutionSuccess{
		executionToken: toolCall.Token.token,
		normalized:     outcome.(*ToolExecutionSuccess),
	}, nil
}

func (bodyDispatcher *dispatcher) normalize(
	toolCall ToolExecution,
	entry *registeredTool,
	candidate ToolExecutionResult,
) (ToolExecutionResult, error) {
	if candidate == nil {
		return nil, errors.New("tools: execute handler returned a nil result")
	}
	if canonical, matches := candidate.(*canonicalToolExecutionSuccess); matches {
		if canonical.executionToken == toolCall.Token.token {
			return canonical.cloneResult()
		}
		return bodyDispatcher.normalizeAuthoredSuccess(
			toolCall,
			entry,
			canonical.normalized,
		)
	}
	if succeeded, matches := candidate.(*ToolExecutionSuccess); matches {
		return bodyDispatcher.normalizeAuthoredSuccess(
			toolCall,
			entry,
			succeeded,
		)
	}
	if failedOutcome, matches := candidate.(*ToolExecutionFailure); matches {
		return failedOutcome.cloneResult()
	}
	return nil, fmt.Errorf(
		"tools: unsupported execution result %T",
		candidate,
	)
}

func (bodyDispatcher *dispatcher) normalizeAuthoredSuccess(
	toolCall ToolExecution,
	entry *registeredTool,
	authored *ToolExecutionSuccess,
) (ToolExecutionResult, error) {
	if authored == nil {
		return nil, errors.New("tools: execute handler returned a nil success")
	}
	if entry == nil {
		return nil, &ToolNotFoundError{
			Name: toolCall.Name,
		}
	}
	normalized, err := bodyDispatcher.success(
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

func (bodyDispatcher *dispatcher) success(
	toolCall ToolExecution,
	entry *registeredTool,
	candidate json.RawMessage,
) (ToolExecutionResult, error) {
	value, err := jsonvalue.Clone(candidate)
	if err != nil {
		return nil, outputError(
			toolCall.Name,
			"value is not lossless JSON: "+err.Error(),
		)
	}
	if err := validateSchemaValue(
		entry.outputSchema,
		value,
		"value",
	); err != nil {
		return nil, outputError(toolCall.Name, err.Error())
	}
	content, err := invokeRenderer(
		entry.definition.Output.Renderer,
		toolCall.arguments,
		value,
	)
	if err != nil {
		return nil, outputError(
			toolCall.Name,
			"output.render failed: "+err.Error(),
		)
	}
	content, err = llm.CloneContentBlocks(content)
	if err != nil {
		return nil, outputError(
			toolCall.Name,
			"output.render returned invalid content: "+err.Error(),
		)
	}
	var meta json.RawMessage
	if toolCall.Parent.IsZero() &&
		entry.definition.Output.PresentationMeta != nil {
		meta, err = invokeProjector(
			entry.definition.Output.PresentationMeta,
			toolCall.arguments,
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
	return &ToolExecutionSuccess{
		Value:         value,
		Content:       content,
		Meta:          meta,
		ConcludesTurn: stateSnapshot.concludesTurn,
	}, nil
}

func outputError(name string, violation string) *ToolOutputError {
	return &ToolOutputError{
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
	if cause := context.Cause(requestContext); cause != nil {
		return cause
	}
	return requestContext.Err()
}

func invokeExecutor(
	body Executor,
	arguments json.RawMessage,
	runContext ToolRunContext,
) (value json.RawMessage, invokeErr error) {
	defer func() {
		if panicValue := recover(); panicValue != nil {
			invokeErr = fmt.Errorf(
				"tools: executor panicked: %v",
				panicValue,
			)
		}
	}()
	return body.Execute(
		append(json.RawMessage(nil), arguments...),
		runContext,
	)
}

func invokeRenderer(
	renderer OutputRenderer,
	arguments json.RawMessage,
	value json.RawMessage,
) (content []llm.ContentBlock, invokeErr error) {
	defer func() {
		if panicValue := recover(); panicValue != nil {
			invokeErr = fmt.Errorf("%v", panicValue)
		}
	}()
	return renderer.Render(
		append(json.RawMessage(nil), arguments...),
		append(json.RawMessage(nil), value...),
	)
}

func invokeProjector(
	projector PresentationProjector,
	arguments json.RawMessage,
	value json.RawMessage,
) (meta json.RawMessage, invokeErr error) {
	defer func() {
		if panicValue := recover(); panicValue != nil {
			invokeErr = fmt.Errorf("%v", panicValue)
		}
	}()
	return projector.Project(
		append(json.RawMessage(nil), arguments...),
		append(json.RawMessage(nil), value...),
	)
}
