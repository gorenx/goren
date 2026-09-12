package promptactor

import (
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/systemprompt"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// InitializeRuntime asks the Runtime to obtain its definitions from their owners.
type InitializeRuntime struct {
	// Config selects the initial baseline independently of the definition source.
	// Nil uses the Registry baseline for initialization.
	Config  *systemprompt.Config
	AgentID session.SessionID
	// PromptRegistry owns registered prompt sections, contexts and variables.
	// The Runtime requests Prepare to obtain their definition snapshot; it does
	// not ask this Registry to assemble the Agent's model input.
	PromptRegistry *goakt.GrainIdentity
	// ToolsRuntime provides only successfully applied tool schemas.
	ToolsRuntime *goakt.GrainIdentity
	// CreationID authorizes this Agent lifetime's tool baseline read.
	CreationID string
	// Sections are local definitions captured for this Agent lifetime.
	Sections []systemprompt.PromptSection
}

// ApplyTools installs a committed tool view without switching shared prompt definitions.
// The baseline selected at initialization is retained.
type ApplyTools struct {
	AgentID     session.SessionID
	Definitions systemprompt.ToolDefinitions
}

// ApplyConfiguration installs the acknowledged tools and refreshes shared prompt
// definitions at a Turn boundary. Local sections and active snapshots are retained.
type ApplyConfiguration struct {
	AgentID session.SessionID

	Definitions systemprompt.ToolDefinitions
}

// Assemble requests one prompt using the installed tool baseline.
type Assemble struct {
	ID string
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
	Err error
}

// ReadRuntime reads the baseline of an already-initialized Prompt runtime.
type ReadRuntime struct{}

// RuntimeView confirms an initialized Prompt runtime without assembling a prompt.
type RuntimeView struct {
	Config systemprompt.Config
	Err    error
}

// RuntimeName returns this service's stable address within a Session lifetime.
// Business identity is carried explicitly in messages, never parsed from this name.
func RuntimeName(identifier string) string { return identifier + ".prompt" }
