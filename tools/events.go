package tools

import (
	"context"
	"errors"
	"fmt"

	"github.com/gorenx/goren/plugin"
)

// PreExecuteNext delegates to the remaining pre-dispatch policy chain.
type PreExecuteNext func(context.Context) (PreToolDecision, error)

// PreExecuteHandler may allow, deny, or request approval for one call.
type PreExecuteHandler func(context.Context, ToolExecution, PreExecuteNext) (PreToolDecision, error)

// ExecuteNext delegates to the remaining around-dispatch chain. The supplied
// context becomes the next wrapper's context and is fused with caller
// cancellation before the tool body runs.
type ExecuteNext func(context.Context) (ToolExecutionResult, error)

// ExecuteHandler wraps dispatch for timeout, retry, metrics, or interception.
type ExecuteHandler func(context.Context, ToolExecution, ExecuteNext) (ToolExecutionResult, error)

// PostExecuteNext delegates to the remaining post-dispatch policy chain.
type PostExecuteNext func(context.Context) (PostToolDecision, error)

// PostExecuteHandler may accept, replace one projection, or block an outcome.
type PostExecuteHandler func(context.Context, ToolExecution, ToolResultSnapshot, PostExecuteNext) (PostToolDecision, error)

// ResultHandler observes one detached authoritative final outcome.
type ResultHandler func(context.Context, ToolExecution, ToolResultSnapshot) error

// ChangeHandler observes registry visibility changes.
type ChangeHandler func(context.Context) error

type postExecutePayload struct {
	execution ToolExecution
	snapshot  ToolResultSnapshot
}

type resultPayload struct {
	execution ToolExecution
	snapshot  ToolResultSnapshot
}

var (
	preExecuteEvent  = plugin.DefineEvent[ToolExecution, PreToolDecision](PreExecuteEventName, plugin.ModeWaterfall)
	executeEvent     = plugin.DefineEvent[ToolExecution, ToolExecutionResult](ExecuteEventName, plugin.ModeWaterfall)
	postExecuteEvent = plugin.DefineEvent[postExecutePayload, PostToolDecision](PostExecuteEventName, plugin.ModeWaterfall)
	resultEvent      = plugin.DefineEvent[resultPayload, struct{}](ResultEventName, plugin.ModeEmit)
	changeEvent      = plugin.DefineEvent[struct{}, struct{}](ChangeEventName, plugin.ModeEmit)
)

// OnPreExecute registers a scope-owned pre-dispatch policy wrapper.
func OnPreExecute(pluginScope *plugin.Scope, callback PreExecuteHandler) (plugin.Disposer, error) {
	if callback == nil {
		return nil, errors.New("tools: pre-execute handler is nil")
	}
	return plugin.OnWaterfall(pluginScope, preExecuteEvent,
		func(requestContext context.Context, execution ToolExecution, downstream plugin.Next[ToolExecution, PreToolDecision]) (PreToolDecision, error) {
			return invokePreHandler(callback, requestContext, execution, func(chainContext context.Context) (PreToolDecision, error) {
				return downstream(chainContext, execution)
			})
		})
}

// OnExecute registers a scope-owned around-dispatch wrapper.
func OnExecute(pluginScope *plugin.Scope, callback ExecuteHandler) (plugin.Disposer, error) {
	if callback == nil {
		return nil, errors.New("tools: execute handler is nil")
	}
	return plugin.OnWaterfall(pluginScope, executeEvent,
		func(requestContext context.Context, execution ToolExecution, downstream plugin.Next[ToolExecution, ToolExecutionResult]) (ToolExecutionResult, error) {
			return invokeExecuteHandler(callback, requestContext, execution, func(chainContext context.Context) (ToolExecutionResult, error) {
				return downstream(chainContext, execution)
			})
		})
}

// OnPostExecute registers a scope-owned post-dispatch policy wrapper.
func OnPostExecute(pluginScope *plugin.Scope, callback PostExecuteHandler) (plugin.Disposer, error) {
	if callback == nil {
		return nil, errors.New("tools: post-execute handler is nil")
	}
	return plugin.OnWaterfall(pluginScope, postExecuteEvent,
		func(requestContext context.Context, payload postExecutePayload, downstream plugin.Next[postExecutePayload, PostToolDecision]) (PostToolDecision, error) {
			return invokePostHandler(callback, requestContext, payload.execution, payload.snapshot,
				func(chainContext context.Context) (PostToolDecision, error) {
					return downstream(chainContext, payload)
				})
		})
}

// OnResult registers a scope-filtered final-outcome observer.
func OnResult(pluginScope *plugin.Scope, callback ResultHandler) (plugin.Disposer, error) {
	if callback == nil {
		return nil, errors.New("tools: result handler is nil")
	}
	return plugin.OnNotify(pluginScope, resultEvent, func(requestContext context.Context, payload resultPayload) error {
		return invokeResultHandler(callback, requestContext, payload.execution, payload.snapshot)
	})
}

// OnChange registers an unfiltered registry-change observer.
func OnChange(pluginScope *plugin.Scope, callback ChangeHandler) (plugin.Disposer, error) {
	if callback == nil {
		return nil, errors.New("tools: change handler is nil")
	}
	return plugin.OnNotify(pluginScope, changeEvent, func(requestContext context.Context, _ struct{}) error {
		return invokeChangeHandler(callback, requestContext)
	})
}

func invokePreHandler(
	callback PreExecuteHandler,
	requestContext context.Context,
	execution ToolExecution,
	downstream PreExecuteNext,
) (decision PreToolDecision, invokeErr error) {
	defer containHandlerPanic("pre-execute", &invokeErr)
	return callback(requestContext, execution, downstream)
}

func invokeExecuteHandler(
	callback ExecuteHandler,
	requestContext context.Context,
	execution ToolExecution,
	downstream ExecuteNext,
) (outcome ToolExecutionResult, invokeErr error) {
	defer containHandlerPanic("execute", &invokeErr)
	return callback(requestContext, execution, downstream)
}

func invokePostHandler(
	callback PostExecuteHandler,
	requestContext context.Context,
	execution ToolExecution,
	snapshot ToolResultSnapshot,
	downstream PostExecuteNext,
) (decision PostToolDecision, invokeErr error) {
	defer containHandlerPanic("post-execute", &invokeErr)
	return callback(requestContext, execution, snapshot, downstream)
}

func invokeResultHandler(
	callback ResultHandler,
	requestContext context.Context,
	execution ToolExecution,
	snapshot ToolResultSnapshot,
) (invokeErr error) {
	defer containHandlerPanic("result", &invokeErr)
	return callback(requestContext, execution, snapshot)
}

func invokeChangeHandler(callback ChangeHandler, requestContext context.Context) (invokeErr error) {
	defer containHandlerPanic("change", &invokeErr)
	return callback(requestContext)
}

func containHandlerPanic(label string, invokeErr *error) {
	if panicValue := recover(); panicValue != nil {
		*invokeErr = fmt.Errorf("tools: %s handler panicked: %v", label, panicValue)
	}
}
