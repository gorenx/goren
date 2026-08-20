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
	Failed() bool
	ContentBlocks() []llm.ContentBlock
	AdditionalContextMessages() []llm.UserMessage
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
	owner              *toolExecutionToken
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
