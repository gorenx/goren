package tools

import (
	"encoding/json"
	"time"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/llm"
)

// Executor runs one already-snapshotted tool call and returns a canonical JSON
// value. Implementations must observe ToolRunContext.Context and settle owned
// work before returning.
type Executor interface {
	Execute(arguments json.RawMessage, runContext ToolRunContext) (value json.RawMessage, err error)
}

// OutputRenderer projects validated arguments and a validated canonical value
// into model-facing content.
type OutputRenderer interface {
	Render(arguments json.RawMessage, value json.RawMessage) (content []agentmessage.ContentBlock, err error)
}

// PresentationProjector derives replayable tool-private UI metadata from one
// top-level successful value.
type PresentationProjector interface {
	Project(arguments json.RawMessage, value json.RawMessage) (metadata json.RawMessage, err error)
}

// ContentFinalizer performs the snapshotted definition-owned last-mile
// content transform. replaceContent=false preserves the supplied content.
type ContentFinalizer interface {
	Finalize(toolCall ToolExecution, resultView ToolResultSnapshot) (content []agentmessage.ContentBlock, replaceContent bool)
}

// ConcurrencyClassifier opts a call into overlap with sibling tool calls.
// False, absence, or panic is fail-closed exclusive scheduling.
type ConcurrencyClassifier interface {
	ConcurrencySafe(arguments json.RawMessage) (safe bool)
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

// Definitions is a detached model-facing view of one effective tool catalog.
// KnownNames includes names hidden by restrictions so presentation can validate
// configured ordering without exposing those tools in Schemas.
type Definitions struct {
	Schemas    []llm.ToolSchema
	KnownNames []string
}
