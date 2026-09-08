package promptactor

import (
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/systemprompt"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// InitializeRuntime asks the Runtime to obtain its definitions from their owners.
type InitializeRuntime struct {
	AgentID session.SessionID
	// PromptRegistry owns registered prompt sections, contexts and variables.
	// The Runtime requests Prepare to obtain their definition snapshot; it does
	// not ask this Registry to assemble the Agent's model input.
	PromptRegistry *goakt.GrainIdentity
	// ToolsRegistry provides the Agent's committed tool schemas and version.
	ToolsRegistry *goakt.GrainIdentity
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

// RuntimeInitialized confirms both prompt definitions and the tool baseline.
type RuntimeInitialized struct {
	ToolsVersion uint64
	Err          error
}
