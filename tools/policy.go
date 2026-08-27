package tools

import (
	"encoding/json"

	"github.com/gorenx/goren/agentmessage"
)

// PreToolDecision is the closed pre-dispatch policy union.
type PreToolDecision interface {
	preToolDecision()
}

// AllowDecision permits dispatch.
type AllowDecision struct{}

func (AllowDecision) preToolDecision() {}

// DenyDecision rejects dispatch with model-visible feedback.
type DenyDecision struct {
	Reason string
}

func (DenyDecision) preToolDecision() {}

// AskDecision requires approval; without an approval provider it degrades to denial.
type AskDecision struct {
	Reason string
}

func (AskDecision) preToolDecision() {}

// PostToolDecision is the closed post-dispatch policy union.
type PostToolDecision interface {
	postToolDecision()
}

// AcceptDecision preserves the normalized result and may append next-step context.
type AcceptDecision struct {
	AdditionalContexts []agentmessage.UserMessage
}

func (AcceptDecision) postToolDecision() {}

// ReplaceContentDecision replaces only the model-facing projection.
type ReplaceContentDecision struct {
	Content            []agentmessage.ContentBlock
	AdditionalContexts []agentmessage.UserMessage
}

func (ReplaceContentDecision) postToolDecision() {}

// ReplaceValueDecision replaces a successful canonical value and re-renders it.
type ReplaceValueDecision struct {
	Value              json.RawMessage
	AdditionalContexts []agentmessage.UserMessage
}

func (ReplaceValueDecision) postToolDecision() {}

// BlockDecision turns corrective feedback into a failed result.
type BlockDecision struct {
	Feedback           []agentmessage.ContentBlock
	AdditionalContexts []agentmessage.UserMessage
}

func (BlockDecision) postToolDecision() {}

// ToolRestriction intersects inherited capability visibility. A nil field is
// absent; a non-nil empty Allow intentionally admits no inherited tools.
type ToolRestriction struct {
	Allow []string
	Deny  []string
}

// ToolGuard is a monotonic synchronous denial policy.
type ToolGuard interface {
	DenyReason(ToolExecution) (string, bool)
}

// ToolGuardFunc adapts a function to ToolGuard.
type ToolGuardFunc func(ToolExecution) (string, bool)

// DenyReason invokes the adapted function.
func (operation ToolGuardFunc) DenyReason(toolCall ToolExecution) (string, bool) {
	return operation(toolCall)
}
