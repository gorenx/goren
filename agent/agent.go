// Package agent owns the live Agent capability, Registry, durable Inbox
// projection, and Agent-scoped extension contracts.
package agent

import (
	"context"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
)

const (
	// PluginName is the canonical Harness Agent Registry Plugin name.
	PluginName = "@deepseek-ai/dsh-agent"
	// ServiceName preserves the canonical Cordis capability name for
	// diagnostics and source traceability.
	ServiceName = "agents"
)

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

// Options contains the provider-neutral configuration applied to one Agent.
type Options struct {
	Provider  string
	Model     string
	MaxTokens *int
}

// MaintenanceTask is one non-turn task that may run only from true idle.
type MaintenanceTask interface {
	Run(context.Context) error
}

// MaintenanceFunc adapts a naturally stateless operation to MaintenanceTask.
type MaintenanceFunc func(context.Context) error

func (operation MaintenanceFunc) Run(requestContext context.Context) error {
	return operation(requestContext)
}

// Agent is both a scoped runtime Plugin and the live business capability
// returned by Registry. Consumers do not receive its Fiber or Scope.
type Agent interface {
	plugin.Plugin
	plugin.Service
	ID() session.SessionID
	OptionsValue() Options
	SessionValue() *session.Session
	InboxValue() *Inbox
	StatusValue() Status
	Cancel(CancelCause, CancelOptions)
	WhenIdle(context.Context) error
	RunMaintenance(context.Context, MaintenanceTask) error
	Send(llm.UserMessage, InboxTarget, bool) error
	Followup(llm.UserMessage) error
	Steer(llm.UserMessage) error
	Inject(llm.UserMessage) error
}
