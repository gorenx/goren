package tools

import (
	"context"
)

// ResultObserver observes final Tool results after one call completes.
// Implementations are ordinary business objects, not Plugin observers.
type ResultObserver interface {
	ObserveToolResult(context.Context, ExecutionCompleted) error
}

// PreExecuteRequest is the immutable input of the pre-dispatch policy. Middleware may decide, deny, ask, or delegate.
type PreExecuteRequest struct {
	toolCall ToolExecution
}

// Execution returns a detached view of the call being considered.
func (input PreExecuteRequest) Execution() ToolExecution {
	return cloneExecution(input.toolCall)
}

// PreExecuteOutcome is the typed pre-dispatch policy result.
type PreExecuteOutcome struct {
	Decision PreToolDecision
}

// ExecuteRequest is the immutable input of the around-dispatch policy.
// Middleware may replace only the context passed to the downstream Action.
type ExecuteRequest struct {
	toolCall ToolExecution
}

// Execution returns a detached view of the call being dispatched.
func (input ExecuteRequest) Execution() ToolExecution {
	return cloneExecution(input.toolCall)
}

// ExecuteOutcome is the typed around-dispatch result.
type ExecuteOutcome struct {
	Result ToolExecutionResult
}

// PostExecuteRequest contains the immutable execution and detached result seen
// by post-dispatch policy.
type PostExecuteRequest struct {
	toolCall   ToolExecution
	resultView ToolResultSnapshot
}

// Execution returns a detached view of the completed call.
func (input PostExecuteRequest) Execution() ToolExecution {
	return cloneExecution(input.toolCall)
}

// Result returns the read-only detached outcome snapshot.
func (input PostExecuteRequest) Result() ToolResultSnapshot {
	return input.resultView
}

// PostExecuteOutcome is the typed post-dispatch policy result.
type PostExecuteOutcome struct {
	Decision PostToolDecision
}

// ExecutionCompleted is the final, immutable tools/result Runtime Event.
type ExecutionCompleted struct {
	toolCall   ToolExecution
	resultView ToolResultSnapshot
}

// EventName returns the canonical Harness Event name.
func (ExecutionCompleted) EventName() string {
	return ResultEventName
}

// Execution returns a detached view of the final call.
func (completed ExecutionCompleted) Execution() ToolExecution {
	return cloneExecution(completed.toolCall)
}

// Result returns the read-only final outcome snapshot.
func (completed ExecutionCompleted) Result() ToolResultSnapshot {
	return completed.resultView
}

// DefinitionsTopic carries complete, committed tool definition views.
const DefinitionsTopic = "tools/definitions"

// NewPreExecuteRequest captures input for pre-dispatch policy.
func NewPreExecuteRequest(input ToolExecution) PreExecuteRequest {
	return PreExecuteRequest{
		toolCall: cloneExecution(input),
	}
}

// NewExecuteRequest captures input for execution middleware.
func NewExecuteRequest(input ToolExecution) ExecuteRequest {
	return ExecuteRequest{
		toolCall: cloneExecution(input),
	}
}

// NewPostExecuteRequest combines detached input with an immutable result view.
func NewPostExecuteRequest(input ToolExecution, view ToolResultSnapshot) PostExecuteRequest {
	return PostExecuteRequest{
		toolCall:   cloneExecution(input),
		resultView: view,
	}
}

// NewExecutionCompleted combines detached input with an immutable final result view.
func NewExecutionCompleted(input ToolExecution, view ToolResultSnapshot) ExecutionCompleted {
	return ExecutionCompleted{
		toolCall:   cloneExecution(input),
		resultView: view,
	}
}
