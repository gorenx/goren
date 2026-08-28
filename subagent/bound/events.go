package bound

import (
	"github.com/gorenx/goren/session"
)

const (
	// BindingEventName records one Definition-to-child binding in a user
	// Session and freezes the parent context copied by a fresh child.
	BindingEventName = "subagent/bound-binding"
	// DefinitionAppliedEventName records the complete Definition actually
	// installed in one exact Bound child Agent epoch.
	DefinitionAppliedEventName = "subagent/bound-definition-applied"
	// MaterializationEventName records one Bound child create or restore attempt
	// in the owning user Session.
	MaterializationEventName = "subagent/bound-materialization"
	// CursorEventName records the half-open parent Session prefix already skipped
	// or durably admitted to one Bound child Inbox.
	CursorEventName = "subagent/bound-cursor"
	// EventVersion rejects the removed per-parent configuration contract.
	EventVersion = 2
)

// BindingData is the immutable owner-defined parent event payload.
type BindingData struct {
	Version        int               `json:"version"`
	Name           string            `json:"name"`
	ChildSessionID session.SessionID `json:"childSessionId"`
	ContextNextSeq int64             `json:"contextNextSeq"`
}

// DefinitionAppliedData is the child-owned reconstruction evidence for the
// exact Definition installed before the Agent epoch was published.
type DefinitionAppliedData struct {
	Version    int        `json:"version"`
	Definition Definition `json:"definition"`
}

// MaterializationResult classifies one durable child create or restore attempt
// without persisting diagnostics.
type MaterializationResult string

const (
	MaterializationSucceeded MaterializationResult = "succeeded"
	MaterializationFailed    MaterializationResult = "failed"
)

// MaterializationData records one attempt against a stable Binding.
type MaterializationData struct {
	Version            int                   `json:"version"`
	Name               string                `json:"name"`
	ChildSessionID     session.SessionID     `json:"childSessionId"`
	DefinitionRevision int64                 `json:"definitionRevision"`
	Result             MaterializationResult `json:"result"`
}

// CursorDisposition classifies why one parent prefix may advance.
type CursorDisposition string

const (
	// CursorDelivered means the exact interaction receipt is durable in the
	// child Session before this parent progress fact commits.
	CursorDelivered CursorDisposition = "delivered"
	// CursorSkipped means the completed parent turn contained no direct user
	// interaction and therefore produced no child message.
	CursorSkipped CursorDisposition = "skipped"
)

// Cursor is one monotonic per-Binding parent interaction cursor. NextSeq is
// half-open: every parent event before it was handled.
type Cursor struct {
	Version         int               `json:"version"`
	Name            string            `json:"name"`
	ChildSessionID  session.SessionID `json:"childSessionId"`
	PreviousNextSeq int64             `json:"previousNextSeq"`
	NextSeq         int64             `json:"nextSeq"`
	ThroughTurn     int64             `json:"throughTurn"`
	Disposition     CursorDisposition `json:"disposition"`
}

var BindingEvent = session.DefineEvent[BindingData](BindingEventName)
var DefinitionAppliedEvent = session.DefineEvent[DefinitionAppliedData](
	DefinitionAppliedEventName,
)
var MaterializationEvent = session.DefineEvent[MaterializationData](
	MaterializationEventName,
)
var CursorEvent = session.DefineEvent[Cursor](CursorEventName)
