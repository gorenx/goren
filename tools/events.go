package tools

import "github.com/gorenx/goren/plugin"

// PreExecuteRequest is the immutable input of the pre-dispatch policy
// Waterfall. Middleware may decide, deny, ask, or delegate.
type PreExecuteRequest struct {
	plugin.WaterfallInputBase
	toolCall ToolExecution
}

// Execution returns a detached view of the call being considered.
func (input PreExecuteRequest) Execution() ToolExecution {
	return cloneExecution(input.toolCall)
}

// PreExecuteOutcome is the typed pre-dispatch policy result.
type PreExecuteOutcome struct {
	plugin.WaterfallOutputBase
	Decision PreToolDecision
}

// ExecuteRequest is the immutable input of the around-dispatch Waterfall.
// Middleware may replace only the context passed to the downstream Action.
type ExecuteRequest struct {
	plugin.WaterfallInputBase
	toolCall ToolExecution
}

// Execution returns a detached view of the call being dispatched.
func (input ExecuteRequest) Execution() ToolExecution {
	return cloneExecution(input.toolCall)
}

// ExecuteOutcome is the typed around-dispatch result.
type ExecuteOutcome struct {
	plugin.WaterfallOutputBase
	Result ToolExecutionResult
}

// PostExecuteRequest contains the immutable execution and detached result seen
// by post-dispatch policy.
type PostExecuteRequest struct {
	plugin.WaterfallInputBase
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
	plugin.WaterfallOutputBase
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

// EventDelivery contains observer failures outside the authoritative Tool
// outcome while reporting them through Runtime's EventFailureReporter.
func (ExecutionCompleted) EventDelivery() plugin.DeliveryPolicy {
	return plugin.DeliveryBestEffort
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

// EventDelivery keeps registry mutation and notification ordered. A failed
// observer rejects the mutation so callers never observe a silent partial add.
func (RegistryChanged) EventDelivery() plugin.DeliveryPolicy {
	return plugin.DeliveryOrdered
}
