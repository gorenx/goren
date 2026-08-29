// Package agent owns the live Agent capability, Registry, durable Inbox
// projection, and Agent-scoped extension contracts.
package agent

import (
	"context"
	"reflect"

	"github.com/gorenx/goren/agentmessage"
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

// Agent is the live business capability returned by Registry. Plugin identity
// and structural Scope ownership remain private to the Agent Loop adapter.
type Agent interface {
	ID() session.SessionID
	OptionsValue() Options
	SessionValue() session.Context
	InboxValue() *Inbox
	StatusValue() Status
	Cancel(CancelCause, CancelOptions)
	WhenIdle(context.Context) error
	RunMaintenance(context.Context, func(context.Context) error) error
	Followup(agentmessage.UserMessage) error
	Steer(agentmessage.UserMessage) error
	Inject(agentmessage.UserMessage) error
}

// Same reports whether both values are the exact same process-local Agent
// instance. Durable Agent IDs may be reused by later resumed instances.
func Same(leftSubject Agent, rightSubject Agent) bool {
	if leftSubject == nil || rightSubject == nil ||
		leftSubject.ID() != rightSubject.ID() {
		return false
	}
	leftType := reflect.TypeOf(leftSubject)
	if leftType != reflect.TypeOf(rightSubject) || !leftType.Comparable() {
		return false
	}
	return leftSubject == rightSubject
}
