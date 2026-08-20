package tools

import (
	"encoding/json"
	"time"

	"github.com/gorenx/goren/llm"
)

// Executor runs one already-snapshotted tool call and returns a canonical JSON
// value. Implementations must observe ToolRunContext.Context and settle owned
// work before returning.
type Executor interface {
	Execute(json.RawMessage, ToolRunContext) (json.RawMessage, error)
}

// ExecutorFunc adapts a function to Executor.
type ExecutorFunc func(json.RawMessage, ToolRunContext) (json.RawMessage, error)

// Execute invokes the adapted function.
func (operation ExecutorFunc) Execute(arguments json.RawMessage, runContext ToolRunContext) (json.RawMessage, error) {
	return operation(arguments, runContext)
}

// OutputRenderer projects validated arguments and a validated canonical value
// into model-facing content.
type OutputRenderer interface {
	Render(json.RawMessage, json.RawMessage) ([]llm.ContentBlock, error)
}

// OutputRendererFunc adapts a function to OutputRenderer.
type OutputRendererFunc func(json.RawMessage, json.RawMessage) ([]llm.ContentBlock, error)

// Render invokes the adapted function.
func (operation OutputRendererFunc) Render(arguments json.RawMessage, value json.RawMessage) ([]llm.ContentBlock, error) {
	return operation(arguments, value)
}

// PresentationProjector derives replayable tool-private UI metadata from one
// top-level successful value.
type PresentationProjector interface {
	Project(json.RawMessage, json.RawMessage) (json.RawMessage, error)
}

// PresentationProjectorFunc adapts a function to PresentationProjector.
type PresentationProjectorFunc func(json.RawMessage, json.RawMessage) (json.RawMessage, error)

// Project invokes the adapted function.
func (operation PresentationProjectorFunc) Project(arguments json.RawMessage, value json.RawMessage) (json.RawMessage, error) {
	return operation(arguments, value)
}

// ContentFinalizer performs the snapshotted definition-owned last-mile
// content transform. replace=false preserves the supplied content.
type ContentFinalizer interface {
	Finalize(ToolExecution, ToolResultSnapshot) (content []llm.ContentBlock, replace bool)
}

// ContentFinalizerFunc adapts a function to ContentFinalizer.
type ContentFinalizerFunc func(ToolExecution, ToolResultSnapshot) ([]llm.ContentBlock, bool)

// Finalize invokes the adapted function.
func (operation ContentFinalizerFunc) Finalize(toolCall ToolExecution, resultView ToolResultSnapshot) ([]llm.ContentBlock, bool) {
	return operation(toolCall, resultView)
}

// ConcurrencyClassifier opts a call into overlap with sibling tool calls.
// False, absence, or panic is fail-closed exclusive scheduling.
type ConcurrencyClassifier interface {
	ConcurrencySafe(json.RawMessage) bool
}

// ConcurrencyClassifierFunc adapts a function to ConcurrencyClassifier.
type ConcurrencyClassifierFunc func(json.RawMessage) bool

// ConcurrencySafe invokes the adapted function.
func (operation ConcurrencyClassifierFunc) ConcurrencySafe(arguments json.RawMessage) bool {
	return operation(arguments)
}

// ToolOutputDefinition owns the successful value schema and its projections.
type ToolOutputDefinition struct {
	Schema           json.RawMessage
	Renderer         OutputRenderer
	PresentationMeta PresentationProjector
}

// ToolDefinition combines the model-facing schema with host-only behavior.
type ToolDefinition struct {
	Name                string
	Description         string
	Parameters          json.RawMessage
	Output              ToolOutputDefinition
	Executor            Executor
	FinalizeContent     ContentFinalizer
	Timeout             time.Duration
	ConcurrencyBehavior ConcurrencyClassifier
}
