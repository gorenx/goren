// Package agent owns the live Agent registry, durable inbox projection, and
// agent-scoped extension events. Concrete turn driving belongs to agent-loop.
package agent

import (
	"context"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
)

const ServiceName = "agents"

// Status is the complete live lifecycle vocabulary. Disposal removes an
// Agent from the registry instead of adding a third status.
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

// CancelCause is the stable caller intent attached to one active operation.
type CancelCause interface {
	CancelKind() string
}

// UserCancel identifies an explicit caller cancellation.
type UserCancel struct{}

func (UserCancel) CancelKind() string { return "user" }

// ParentCancel identifies cancellation inherited from a parent Agent.
type ParentCancel struct{}

func (ParentCancel) CancelKind() string { return "parent" }

// DisposedCancel identifies structural Agent teardown.
type DisposedCancel struct{}

func (DisposedCancel) CancelKind() string { return "disposed" }

// HookCancel carries one extension-owned cancellation reason.
type HookCancel struct {
	Reason string
}

func (HookCancel) CancelKind() string { return "hook" }

// CancelOptions controls whether pending inbox work survives cancellation.
type CancelOptions struct {
	KeepInbox bool
}

// MaintenanceTask is one non-turn task that may run only from true idle.
type MaintenanceTask interface {
	Run(context.Context) error
}

// MaintenanceFunc adapts a function to MaintenanceTask.
type MaintenanceFunc func(context.Context) error

func (operation MaintenanceFunc) Run(requestContext context.Context) error {
	return operation(requestContext)
}

// Agent is the live capability shared by Registry, Agent Loop, API adapters,
// and scoped extensions. Session remains the durable source of truth.
type Agent interface {
	ID() session.SessionID
	OptionsValue() Options
	SessionValue() *session.Session
	InboxValue() *Inbox
	StatusValue() Status
	ScopeValue() *plugin.Scope
	Cancel(CancelCause, CancelOptions)
	WhenIdle(context.Context) error
	RunMaintenance(context.Context, MaintenanceTask) error
	Send(llm.UserMessage, InboxTarget, bool) error
	Followup(llm.UserMessage) error
	Steer(llm.UserMessage) error
	Inject(llm.UserMessage) error
}

// SetupCommit validates prepared scoped contributions at publication commit.
type SetupCommit interface {
	Commit() error
}

// Setup composes one unpublished Agent scope. It never drives the Agent.
type Setup interface {
	Apply(context.Context, *plugin.Scope) (SetupCommit, error)
}

// SetupFunc adapts a function to Setup.
type SetupFunc func(context.Context, *plugin.Scope) (SetupCommit, error)

func (operation SetupFunc) Apply(requestContext context.Context, agentScope *plugin.Scope) (SetupCommit, error) {
	return operation(requestContext, agentScope)
}

// CreateOptions contains the shared Agent/Session identity and unpublished setup.
type CreateOptions struct {
	SessionID    session.SessionID
	Metadata     session.Metadata
	Seed         []session.Event
	AgentOptions Options
	Setup        Setup
}

// Handle is the owner capability returned by Factory creation.
type Handle struct {
	Subject Agent
	Release plugin.Disposer
}

// Dispose stops and removes the exact Agent lifecycle owned by this handle.
func (owned Handle) Dispose(closeContext context.Context) error {
	if owned.Release == nil {
		return nil
	}
	return owned.Release(closeContext)
}

// Factory constructs concrete Agent instances and drives their lifecycle.
// The interface belongs to Registry, its consumer.
type Factory interface {
	CreateAgent(context.Context, *plugin.Scope, CreateOptions) (Handle, error)
}

// Registry tracks live Agent membership and delegates creation to Agent Loop.
type Registry interface {
	SetFactory(context.Context, *plugin.Scope, Factory) (plugin.Disposer, error)
	Create(context.Context, *plugin.Scope, CreateOptions) (Handle, error)
	Register(context.Context, *plugin.Scope, Agent, Agent) (plugin.Disposer, error)
	Enter(Agent, Agent) (plugin.Disposer, error)
	Announce(context.Context, Agent) error
	Get(session.SessionID) (Agent, bool)
	IsOwnedBy(session.SessionID, Agent) bool
	List() []Agent
	Roots() []Agent
}

// Service is the canonical Agent registry Service Definition.
var Service = plugin.DefineService[Registry](ServiceName)
