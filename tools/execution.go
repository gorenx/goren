package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/internal/jsonvalue"
	"github.com/gorenx/goren/session"
)

// ToolExecutionInput is one caller-supplied tool call. Context cancellation is
// supplied separately to Execute and cannot be replaced in this value.
type ToolExecutionInput struct {
	CallID     agentmessage.CallID
	RootCallID agentmessage.CallID
	Name       string
	Arguments  json.RawMessage
	SessionID  session.SessionID
}

// ToolExecution is the detached call data visible to policy; it carries no scheduling authority.
type ToolExecution struct {
	CallID     agentmessage.CallID
	RootCallID agentmessage.CallID
	Name       string
	SessionID  session.SessionID
	arguments  json.RawMessage
}

// ArgumentsJSON returns a detached copy of the losslessly materialized input.
func (toolCall ToolExecution) ArgumentsJSON() json.RawMessage {
	return append(json.RawMessage(nil), toolCall.arguments...)
}

// ToolRunContext is handed to the selected body after policy allows dispatch.
type ToolRunContext struct {
	Context   context.Context
	Execution Invocation
	effects   *bodyEffects
}

// DeferContext attaches one plugin-authored message to this toolCall's final
// result. Agent Loop admits deferred messages only after the containing tool
// result has been committed, preserving model call/result adjacency.
func (runContext ToolRunContext) DeferContext(message agentmessage.UserMessage) {
	if runContext.effects != nil {
		runContext.effects.mu.Lock()
		defer runContext.effects.mu.Unlock()
		if !runContext.effects.closed {
			runContext.effects.contexts = append(runContext.effects.contexts, message)
		}
	}
}

// ConcludeTurn marks this successful execution as terminal for the current
// agent turn. Failed outcomes never carry the marker.
func (runContext ToolRunContext) ConcludeTurn() {
	if runContext.effects != nil {
		runContext.effects.mu.Lock()
		defer runContext.effects.mu.Unlock()
		if !runContext.effects.closed {
			runContext.effects.concludesTurn = true
		}
	}
}

// NewExecution validates and detaches the policy-visible call input.
func NewExecution(input ToolExecutionInput) (ToolExecution, error) {
	rootCallID := input.RootCallID
	if rootCallID == "" {
		rootCallID = input.CallID
	}
	toolCall := ToolExecution{
		CallID:     input.CallID,
		RootCallID: rootCallID,
		Name:       input.Name,
		SessionID:  input.SessionID,
	}
	if strings.TrimSpace(input.Name) == "" ||
		input.Name != strings.TrimSpace(input.Name) {
		return toolCall, errors.New(
			"tools: tool name must be non-empty and trimmed",
		)
	}
	if input.CallID == "" {
		return toolCall, errors.New("tools: call ID is empty")
	}
	arguments, err := jsonvalue.Clone(input.Arguments)
	if err != nil {
		return toolCall, fmt.Errorf(
			"tools: arguments are not lossless JSON: %w",
			err,
		)
	}
	toolCall.arguments = arguments
	return toolCall, nil
}

func cloneExecution(source ToolExecution) ToolExecution {
	// Extension views contain only detached business data.
	source.arguments = append(
		json.RawMessage(nil),
		source.arguments...,
	)
	return source
}

// ToolExecutionMode is the scheduler's fail-closed overlap classification.
type ToolExecutionMode string

const (
	// ExecutionParallel may overlap with sibling calls.
	ExecutionParallel ToolExecutionMode = "parallel"
	// ExecutionExclusive runs alone and forms an ordering barrier.
	ExecutionExclusive ToolExecutionMode = "exclusive"
)

// ScheduledToolStage identifies which ordered scheduler stage owns a prepared call.
type ScheduledToolStage string

const (
	// ScheduledApproval requires an audited external approval before dispatch.
	ScheduledApproval ScheduledToolStage = "approval"
	// ScheduledDispatch means only the body/around-dispatch stage may overlap.
	ScheduledDispatch ScheduledToolStage = "dispatch"
	// ScheduledPostResult means the result still requires ordered post policy and finalization.
	ScheduledPostResult ScheduledToolStage = "post-result"
	// ScheduledFinalResult means policy failed terminally and only finalization remains.
	ScheduledFinalResult ScheduledToolStage = "final-result"
)
