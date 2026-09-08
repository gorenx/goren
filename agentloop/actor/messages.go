// Package loopactor owns the asynchronous execution boundary of one Agent.
package loopactor

import (
	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
)

// Completion reports runtime operation failure or success.
type Completion struct {
	Err error
}

// GetInbox requests detached pending input.
type GetInbox struct{}

// Inbox returns pending input without exposing its mutable business owner.
type Inbox struct {
	NextTurn []agentmessage.UserMessage
	NextStep []agentmessage.UserMessage
	Err      error
}

// Followup appends one prompt for a new turn.
type Followup struct {
	CreationID string
	Message    agentmessage.UserMessage
}

// Steer appends one prompt for the next step.
type Steer struct {
	CreationID string
	Message    agentmessage.UserMessage
}

// Inject appends next-Step input without requesting execution.
type Inject struct {
	CreationID string
	Message    agentmessage.UserMessage
}

// Replace changes pending input under its existing message identity.
type Replace struct {
	CreationID string
	MessageID  agentmessage.MessageID
	Message    agentmessage.UserMessage
}

// Remove cancels pending input with the selected identity.
type Remove struct {
	CreationID string
	MessageID  agentmessage.MessageID
}

// Changed acknowledges committed Inbox changes, never an uncommitted proposal.
type Changed struct {
	Changes agent.InboxChanges
	Err     error
}

// Cancel signals the current execution and optionally cancels pending input.
// Changed acknowledges the signal and requested Inbox commit, not execution
// completion or Session durability. A commit error does not undo the signal.
type Cancel struct {
	CreationID string
	Cause      agent.CancelCause
	Options    agent.CancelOptions
}

// WhenIdle waits for execution and resource settlement, not an empty Inbox.
// Injected input can remain pending while an Agent is idle.
type WhenIdle struct {
	CreationID string
}

// RuntimeName returns this service's stable address within a Session lifetime.
// Business identity is carried explicitly in messages, never parsed from this name.
func RuntimeName(identifier string) string { return identifier + ".agentloop" }
