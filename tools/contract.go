// Package tools defines tool contracts, execution policy, and canonical results.
package tools

const (
	// ServiceName is the canonical Cordis service name.
	ServiceName = "tools"
	// PreExecuteEventName is the canonical pre-dispatch policy waterfall.
	PreExecuteEventName = "tools/pre-execute"
	// ExecuteEventName is the canonical around-dispatch waterfall.
	ExecuteEventName = "tools/execute"
	// PostExecuteEventName is the canonical result-policy waterfall.
	PostExecuteEventName = "tools/post-execute"
	// ResultEventName is the canonical final-outcome notification.
	ResultEventName = "tools/result"
	// ChangeEventName is the unfiltered registry-change notification.
	ChangeEventName = "tools/change"
	// ToolAborted is the stable code for cancellation after body invocation.
	ToolAborted = "ABORTED"
	// ToolAbortedBeforeDispatch is the stable code for cancellation before body invocation.
	ToolAbortedBeforeDispatch = "ABORTED_BEFORE_DISPATCH"
	// RunCodeName is reserved for the future Code Mode presentation transport.
	RunCodeName = "run_code"
)
