package promptactor

import (
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/systemprompt"
)

// InitializeRuntime supplies definitions for one Agent's unready Prompt Runtime.
type InitializeRuntime struct {
	AgentID     session.SessionID
	Definitions systemprompt.Snapshot
}

// ApplyTools delivers a detached view of a committed tools event.
type ApplyTools struct {
	AgentID     session.SessionID
	Version     uint64
	Definitions systemprompt.ToolDefinitions
}

// Assemble requests one model input using the applied tool version.
type Assemble struct {
	ID           string
	ToolsVersion uint64
}

// Assembled carries a completed assembly and preserves ordinary Go errors.
type Assembled struct {
	ID     string
	Prompt systemprompt.PromptAssembly
	Err    error
}

// CancelAssembly cancels only the matching active request.
type CancelAssembly struct {
	ID string
}

// CloseRuntime waits for an active assembly to exit before reporting completion.
type CloseRuntime struct{}
