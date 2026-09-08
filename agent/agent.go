// Package agent owns Agent identities, configuration values, durable Inbox
// rules, membership relations, and detached communication contracts.
package agent

import (
	"errors"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
)

// ErrBusy means an exclusive operation cannot start while another activity owns the Agent.
var ErrBusy = errors.New("agent: an activity is already running")

// Status is the complete live lifecycle vocabulary. Disposal removes an
// Agent from the Registry instead of adding a third status.
type Status string

const (
	StatusIdle    Status = "idle"
	StatusRunning Status = "running"
)

// InboxTarget selects one of the two ordered pending-message lists.
type InboxTarget string

const (
	NextTurn InboxTarget = "next-turn"
	NextStep InboxTarget = "next-step"
)

// InboxReader exposes pending-message queries without the owner's mutation methods.
// Implementations return detached messages; this interface does not provide
// synchronization for concurrent access to an actor-owned Inbox.
type InboxReader interface {
	NextTurn() []agentmessage.UserMessage
	NextStep() []agentmessage.UserMessage
	HasPending() bool
}

// Admission contains an accepted business proposal, not a committed input.
// The runtime submits Edit and reports its exact result before waking execution.
type Admission struct {
	Edit InboxEdit
	Wake bool
}

// Agent defines the single Agent's business decisions independently of runtime.
// Input methods prepare durable edits; the runtime acknowledges actual commits.
// Transport cancellation and waiting for idle belong to the actor client.
type Agent interface {
	ID() session.SessionID
	InboxValue() InboxReader
	StatusValue() Status
	Followup(agentmessage.UserMessage) (Admission, error)
	Steer(agentmessage.UserMessage) (Admission, error)
	Inject(agentmessage.UserMessage) (Admission, error)
	Cancel(CancelCause, CancelOptions) (InboxEdit, error)
}

// Pending contains detached inputs waiting for their next execution boundary.
type Pending struct {
	NextTurn []agentmessage.UserMessage
	NextStep []agentmessage.UserMessage
}
