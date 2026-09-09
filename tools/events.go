package tools

import (
	"context"
)

// ResultObserver observes final Tool results inside one plain Tool ToolLayer.
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

// RegistryChanged reports a successful Tool definition or restriction change.
type RegistryChanged struct{}

// EventName returns the canonical Harness Event name.
func (RegistryChanged) EventName() string {
	return ChangeEventName
}

// DefinitionsTopic carries complete, committed tool definition views.
const DefinitionsTopic = "tools/definitions"
