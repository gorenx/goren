package commandactor

import (
	"github.com/gorenx/goren/commands"
	"github.com/gorenx/goren/session"
	goakt "github.com/tochemey/goakt/v4/actor"
	"time"
)

// Initialize binds one Session's command execution routes.
type Initialize struct {
	SessionID  session.SessionID
	CreationID string
	Session    *goakt.GrainIdentity
	Registry   *goakt.GrainIdentity
	Timeout    time.Duration
}

// Completion acknowledges runtime setup or safe closure.
type Completion struct{ Err error }

// Execute requests a direct command without entering the model loop.
type Execute struct {
	CreationID string
	CommandID  commands.ID
	Line       string
	Options    commands.ExecuteOptions
}

// Executed returns nil Execution for an unrecognized command.
type Executed struct {
	Execution *commands.Execution
	Err       error
}

// Cancel signals a specific command; it does not wait for handler cleanup.
type Cancel struct {
	CreationID string
	CommandID  commands.ID
	Cause      error
}

// Cancelled acknowledges cancellation delivery, not handler completion.
type Cancelled struct{ Err error }

// Close requires all admitted calls and their cleanup to have completed.
type Close struct {
	CreationID string
}

// Invoke asks a registered capability Actor to execute a command and reply after cleanup.
type Invoke struct {
	CreationID string
	Input      commands.Invocation
}

// Completed is the final reply to Invoke, after the capability has settled.
type Completed struct {
	Result commands.Result
	Err    error
}

// CancelInvocation conveys cancellation without completing the original Invoke.
type CancelInvocation struct {
	CreationID string
	CommandID  commands.ID
	Cause      error
}

// ListCommands reads this creation's effective directory through its execution owner.
type ListCommands struct {
	CreationID string
}

// Listed contains detached descriptors or an admission/query failure.
type Listed struct {
	Values []commands.Descriptor
	Err    error
}

// RuntimeName returns this service's stable address within a Session lifetime.
// Business identity is carried explicitly in messages, never parsed from this name.
func RuntimeName(identifier string) string { return identifier + ".commands" }
