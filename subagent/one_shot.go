package subagent

import (
	"context"
	"encoding/json"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/tools"
)

// StartRequest is one caller-owned request for a one-shot child.
type StartRequest struct {
	Label        *string
	Prompt       []llm.ContentBlock
	Parent       agent.Agent
	AgentOptions *agent.Options
	OutputSchema json.RawMessage
	MaxDepth     *int64
	ToolFilter   *tools.ToolRestriction
	Persona      *string
}

// ResolvedStartRequest is the Provider-facing one-shot request after Runtime
// validation and descriptor snapshotting.
type ResolvedStartRequest struct {
	StartRequest
	Descriptor OneShotDescriptor
}

// Result is the terminal outcome of one one-shot Run.
type Result struct {
	Output     []llm.ContentBlock
	Structured json.RawMessage
	Diagnostic *string
	StopReason StopReason
}

// StopReason is merge-extensible; consumers must handle unknown values.
type StopReason string

const (
	// StopCompleted means the child finished normally.
	StopCompleted StopReason = "completed"
	// StopAborted means cancellation or disposal stopped the child.
	StopAborted StopReason = "aborted"
	// StopError means a model or transport failure occurred.
	StopError StopReason = "error"
	// StopMaxTokens means the child exhausted its token ceiling.
	StopMaxTokens StopReason = "max-tokens"
	// StopRefusal means the child declined the task.
	StopRefusal StopReason = "refusal"
)

// Run is the holder-owned one-shot child handle returned after publication.
// The context passed to AwaitResult cancels only the wait; Start's context is
// the canonical cancellation channel for the underlying run.
type Run interface {
	ID() session.SessionID
	LocalAgent() (agent.Agent, bool)
	AwaitResult(context.Context) (Result, error)
	Dispose(context.Context) error
}

// OneShotService owns one-shot admission and Run ownership transfer.
type OneShotService interface {
	plugin.Service
	Start(context.Context, string, StartRequest) (Run, error)
}
