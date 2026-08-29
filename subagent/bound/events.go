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

var BindingEvent = session.DefineEvent[BindingData](BindingEventName)
var DefinitionAppliedEvent = session.DefineEvent[DefinitionAppliedData](
	DefinitionAppliedEventName,
)
var MaterializationEvent = session.DefineEvent[MaterializationData](
	MaterializationEventName,
)
