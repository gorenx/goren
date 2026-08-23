package subagent

import (
	"context"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/tools"
)

// ContinuableRequest contains the StartRequest fields valid for continuable
// children. OutputSchema is deliberately absent because there is no Run result.
type ContinuableRequest struct {
	Prompt       []llm.ContentBlock
	Parent       agent.Agent
	AgentOptions *agent.Options
	MaxDepth     *int64
	ToolFilter   *tools.ToolRestriction
	Persona      *string
}

// ContinuableStartSpec requests one durable continuable child.
type ContinuableStartSpec struct {
	Provider string
	Label    string
	ChildID  *session.SessionID
	Request  ContinuableRequest
}

// ContinuableStart contains identities returned once the initial prompt is
// accepted by the child Inbox.
type ContinuableStart struct {
	ChildID   session.SessionID
	MessageID llm.MessageID
}

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

// InterruptAuthority is the closed authorization union for interrupting a
// resident continuable child.
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

// ContinuableService owns durable child admission, delivery, authorization,
// residency, and child-first drainage.
type ContinuableService interface {
	plugin.Service
	StartContinuable(context.Context, ContinuableStartSpec) (ContinuableStart, error)
	Followup(
		context.Context,
		agent.Agent,
		session.SessionID,
		[]llm.ContentBlock,
		FollowupOptions,
	) (llm.MessageID, error)
	Interrupt(session.SessionID, InterruptAuthority) error
	ReportFrom(
		context.Context,
		agent.Agent,
		[]llm.ContentBlock,
		ReportOptions,
	) (llm.MessageID, error)
	DrainContinuableChildren(
		context.Context,
		agent.Agent,
		[]session.SessionID,
	) error
	DrainContinuableDescendants(context.Context, []agent.Agent) error
}
