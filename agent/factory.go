package agent

import (
	"context"

	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
)

// CreateOptions contains the shared Agent/Session identity and unpublished
// Agent composition applied before publication.
type CreateOptions struct {
	SessionID    session.SessionID
	Metadata     session.Metadata
	Seed         []session.Event
	AgentOptions Options
	Provisioner  Provisioner
}

// ResumeOptions contains durable identity and unpublished Agent composition.
type ResumeOptions struct {
	SessionID    session.SessionID
	AgentOptions Options
	Provisioner  Provisioner
}

// Factory is the Registry-owned construction seam implemented by Agent Loop.
type Factory interface {
	CreateAgent(context.Context, plugin.Plugin, CreateOptions) (Handle, error)
	ResumeAgent(context.Context, plugin.Plugin, ResumeOptions) (Handle, error)
}
