package subagent

import (
	"context"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
)

// FollowupOptions carries durable attribution for a later child message.
type FollowupOptions struct {
	Source llm.MessageSource
}

// ReportDelivery controls how an accepted child report schedules its parent.
type ReportDelivery string

const (
	// ReportQuiet appends without waking an idle parent.
	ReportQuiet ReportDelivery = "quiet"
	// ReportNextStep schedules the report for the parent's next model step.
	ReportNextStep ReportDelivery = "next-step"
)

// ReportOptions controls parent scheduling for a child report.
type ReportOptions struct {
	Delivery ReportDelivery
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

// ChildControl sends messages to and interrupts Subagents without exposing
// their exact Agent Handles.
type ChildControl interface {
	Send(
		context.Context,
		agent.Agent,
		session.SessionID,
		[]llm.ContentBlock,
		FollowupOptions,
	) (llm.MessageID, error)
	Interrupt(
		context.Context,
		session.SessionID,
		InterruptAuthority,
	) error
}

// ParentReporter delivers a message from an exact child to its live direct
// parent.
type ParentReporter interface {
	Report(
		context.Context,
		agent.Agent,
		[]llm.ContentBlock,
		ReportOptions,
	) (llm.MessageID, error)
}
