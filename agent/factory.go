package agent

import (
	"context"

	"github.com/gorenx/goren/session"
)

// CreateOptions contains the shared Agent/Session identity and unpublished
// Agent composition applied before publication.
type CreateOptions struct {
	SessionID     session.SessionID
	Metadata      session.Metadata
	Seed          []session.Event
	AgentOptions  Options
	Setup         Setup
	RuntimeParent Agent
}

// ResumeOptions contains durable identity and unpublished Agent composition.
type ResumeOptions struct {
	SessionID     session.SessionID
	AgentOptions  Options
	Setup         Setup
	RuntimeParent Agent
}

// Host owns one constructed Agent, its private Scope, and its single-instance
// runtime lifecycle. It is an ordinary object and never a Plugin.
type Host interface {
	Agent() Agent
	Scope() Scope
	EnterServing(context.Context) error
	Announce(context.Context) error
	Close(context.Context) error
	WhenClosed(context.Context) error
}

// Factory constructs one unpublished Agent Host. It does not reserve Registry
// identities, publish Agent events, or manage descendants.
type Factory interface {
	CreateAgent(context.Context, CreateOptions) (Host, error)
	ResumeAgent(context.Context, ResumeOptions) (Host, error)
}
