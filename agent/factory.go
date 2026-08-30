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

// CreateHostOptions contains only the values needed to construct one fresh
// unpublished Host. Registry-owned Setup and parent relations are excluded.
type CreateHostOptions struct {
	SessionID    session.SessionID
	Metadata     session.Metadata
	Seed         []session.Event
	AgentOptions Options
}

// ResumeHostOptions contains only the values needed to reconstruct one
// unpublished Host from durable Session state.
type ResumeHostOptions struct {
	SessionID    session.SessionID
	AgentOptions Options
}

// Host owns one constructed Agent, its private Scope, and its single-instance
// runtime lifecycle. It is an ordinary object and never a Plugin.
type Host interface {
	Agent() Agent
	Scope() Scope
	EnterServing(context.Context) error
	Announce(context.Context) error
	Close(context.Context) error
}

// Factory constructs one unpublished Agent Host. It does not reserve Registry
// identities, publish Agent events, or manage descendants.
type Factory interface {
	CreateAgent(context.Context, CreateHostOptions) (Host, error)
	ResumeAgent(context.Context, ResumeHostOptions) (Host, error)
}
