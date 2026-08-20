package tools

import (
	"encoding/json"

	"github.com/gorenx/goren/llm"
)

// ToolErrorInfo is stable internal routing metadata for a failed call.
type ToolErrorInfo struct {
	Name string `json:"name"`
	Code string `json:"code"`
}

// ToolFailure is the canonical structured failure detail.
type ToolFailure struct {
	Message string         `json:"message"`
	Info    *ToolErrorInfo `json:"info,omitempty"`
}

// ToolExecutionResult is the closed success/failure outcome union.
type ToolExecutionResult interface {
	ToolResultSnapshot
	cloneResult() (ToolExecutionResult, error)
}

// ToolResultSnapshot is the read-only observer view of one final outcome.
// Every accessor that exposes reference-backed data returns a detached copy.
type ToolResultSnapshot interface {
	Failed() bool
	ContentBlocks() []llm.ContentBlock
	SuccessValue() (json.RawMessage, bool)
	FailureDetail() (ToolFailure, bool)
	PresentationMeta() json.RawMessage
	AdditionalContextMessages() []llm.UserMessage
	ConcludesAgentTurn() bool
}

// ToolExecutionSuccess is a validated canonical value and its projections.
type ToolExecutionSuccess struct {
	Value              json.RawMessage
	Content            []llm.ContentBlock
	Meta               json.RawMessage
	AdditionalContexts []llm.UserMessage
	ConcludesTurn      bool
}

// Failed reports false.
func (*ToolExecutionSuccess) Failed() bool { return false }

// ContentBlocks returns the result's model-facing content.
func (outcome *ToolExecutionSuccess) ContentBlocks() []llm.ContentBlock {
	if outcome == nil {
		return nil
	}
	detached, _ := llm.CloneContentBlocks(outcome.Content)
	return detached
}

// AdditionalContextMessages returns detached next-step context in authored order.
func (outcome *ToolExecutionSuccess) AdditionalContextMessages() []llm.UserMessage {
	if outcome == nil {
		return nil
	}
	detached, _ := cloneUserMessages(outcome.AdditionalContexts)
	return detached
}

// SuccessValue returns a detached canonical value.
func (outcome *ToolExecutionSuccess) SuccessValue() (json.RawMessage, bool) {
	if outcome == nil {
		return nil, false
	}
	return append(json.RawMessage(nil), outcome.Value...), true
}

// FailureDetail reports that a successful result has no failure.
func (*ToolExecutionSuccess) FailureDetail() (ToolFailure, bool) {
	return ToolFailure{}, false
}

// PresentationMeta returns detached tool-private presentation metadata.
func (outcome *ToolExecutionSuccess) PresentationMeta() json.RawMessage {
	if outcome == nil {
		return nil
	}
	return append(json.RawMessage(nil), outcome.Meta...)
}

// ConcludesAgentTurn reports whether this success terminates the current turn.
func (outcome *ToolExecutionSuccess) ConcludesAgentTurn() bool {
	return outcome != nil && outcome.ConcludesTurn
}

// ToolExecutionFailure is a normalized error and its model-facing content.
type ToolExecutionFailure struct {
	Error              ToolFailure
	Content            []llm.ContentBlock
	Meta               json.RawMessage
	AdditionalContexts []llm.UserMessage
}

// Failed reports true.
func (*ToolExecutionFailure) Failed() bool { return true }

// ContentBlocks returns the result's model-facing content.
func (outcome *ToolExecutionFailure) ContentBlocks() []llm.ContentBlock {
	if outcome == nil {
		return nil
	}
	detached, _ := llm.CloneContentBlocks(outcome.Content)
	return detached
}

// AdditionalContextMessages returns detached next-step context in authored order.
func (outcome *ToolExecutionFailure) AdditionalContextMessages() []llm.UserMessage {
	if outcome == nil {
		return nil
	}
	detached, _ := cloneUserMessages(outcome.AdditionalContexts)
	return detached
}

// SuccessValue reports that a failed result has no successful value.
func (*ToolExecutionFailure) SuccessValue() (json.RawMessage, bool) {
	return nil, false
}

// FailureDetail returns a detached structured failure.
func (outcome *ToolExecutionFailure) FailureDetail() (ToolFailure, bool) {
	if outcome == nil {
		return ToolFailure{}, false
	}
	retained := outcome.Error
	if retained.Info != nil {
		detachedInfo := *retained.Info
		retained.Info = &detachedInfo
	}
	return retained, true
}

// PresentationMeta returns detached tool-private presentation metadata.
func (outcome *ToolExecutionFailure) PresentationMeta() json.RawMessage {
	if outcome == nil {
		return nil
	}
	return append(json.RawMessage(nil), outcome.Meta...)
}

// ConcludesAgentTurn reports false for every failed result.
func (*ToolExecutionFailure) ConcludesAgentTurn() bool { return false }

// canonicalToolExecutionSuccess is the immutable terminal result visible to
// execute Middleware. Returning it unchanged preserves the already-normalized
// projection; authored public successes are normalized again.
type canonicalToolExecutionSuccess struct {
	executionToken *toolExecutionToken
	normalized     *ToolExecutionSuccess
}

func (*canonicalToolExecutionSuccess) Failed() bool { return false }

func (outcome *canonicalToolExecutionSuccess) ContentBlocks() []llm.ContentBlock {
	return outcome.normalized.ContentBlocks()
}

func (outcome *canonicalToolExecutionSuccess) SuccessValue() (json.RawMessage, bool) {
	return outcome.normalized.SuccessValue()
}

func (*canonicalToolExecutionSuccess) FailureDetail() (ToolFailure, bool) {
	return ToolFailure{}, false
}

func (outcome *canonicalToolExecutionSuccess) PresentationMeta() json.RawMessage {
	return outcome.normalized.PresentationMeta()
}

func (outcome *canonicalToolExecutionSuccess) AdditionalContextMessages() []llm.UserMessage {
	return outcome.normalized.AdditionalContextMessages()
}

func (outcome *canonicalToolExecutionSuccess) ConcludesAgentTurn() bool {
	return outcome.normalized.ConcludesAgentTurn()
}
