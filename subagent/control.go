package subagent

import (
	"context"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
)

// FollowupOptions carries durable attribution for a later child message.
type FollowupOptions struct {
	Source agentmessage.MessageSource
}

// InterruptAuthority is the closed authorization union for interrupting an
// active child execution.
type InterruptAuthority interface {
	interruptAuthority()
}

// UserInterruptAuthority carries the durable direct-parent address presented
// by a human caller.
type UserInterruptAuthority struct {
	ParentSessionID session.SessionID
}

func (UserInterruptAuthority) interruptAuthority() {}

// AncestorInterruptAuthority carries an exact live ancestor Agent.
type AncestorInterruptAuthority struct {
	Agent agent.Agent
}

func (AncestorInterruptAuthority) interruptAuthority() {}

// ChildControl sends messages to and interrupts child Agents without exposing
// their exact Agent Handles. A resident child receives the message through its
// ordinary Agent Inbox; a durable Continuable child may first be resumed.
type ChildControl interface {
	Send(
		context.Context,
		agent.Agent,
		session.SessionID,
		[]agentmessage.ContentBlock,
		FollowupOptions,
	) (agentmessage.MessageID, error)
	Interrupt(
		context.Context,
		session.SessionID,
		InterruptAuthority,
	) error
}
