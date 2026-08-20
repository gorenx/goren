// Package tools owns tool definitions, layered visibility, execution policy,
// canonical output validation, and model-facing schema projection.
package tools

import (
	"context"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
)

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
	// PluginName is the canonical Harness Tools Plugin name.
	PluginName = "@deepseek-ai/dsh-tools"
	// OverlayPluginName identifies one child Tools registry layer.
	OverlayPluginName = "@deepseek-ai/dsh-tools/overlay"
	// ToolAborted is the stable code for cancellation after body invocation.
	ToolAborted = "ABORTED"
	// ToolAbortedBeforeDispatch is the stable code for cancellation before body invocation.
	ToolAbortedBeforeDispatch = "ABORTED_BEFORE_DISPATCH"
	// RunCodeName is reserved for the future Code Mode presentation transport.
	RunCodeName = "run_code"
)

// ToolRuntime is the provider-owned lookup, scheduling, and execution
// capability consumed by Agent Loop.
type ToolRuntime interface {
	plugin.Service
	ToolExecutionScheduler
	Get(string) (ToolDefinition, bool)
	Schemas() []llm.ToolSchema
	ExecutionMode(ToolExecutionInput) ToolExecutionMode
	Execute(context.Context, ToolExecutionInput) ToolExecutionResult
}

// ToolCatalog is the mutation boundary consumed by Plugins that own Tool
// definitions. It changes only the exact Registry layer resolved by a Plugin.
type ToolCatalog interface {
	plugin.Service
	AddTool(context.Context, ToolDefinition) error
	RemoveTool(context.Context, string) error
}

// PolicyRegistry is the mutation boundary consumed by Plugins that own Tool
// visibility or execution policies. It changes only the exact Registry layer
// resolved by a Plugin.
type PolicyRegistry interface {
	plugin.Service
	AddRestriction(context.Context, string, ToolRestriction) error
	RemoveRestriction(context.Context, string) error
	AddGuard(context.Context, string, ToolGuard) error
	RemoveGuard(context.Context, string) error
}
