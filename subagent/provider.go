package subagent

import (
	"context"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
)

const (
	// PluginName is the canonical Harness Subagent Runtime Plugin name.
	PluginName = "@deepseek-ai/dsh-subagent"
	// ServiceName preserves the canonical Cordis capability name for source
	// traceability and diagnostics.
	ServiceName = "subagents"
)

// Capabilities declares the optional one-shot inputs a Provider implements.
// Continuable support is represented separately by ContinuableProvider.
type Capabilities struct {
	OutputSchema bool
	DepthLimit   bool
	ToolFilter   bool
	Persona      bool
}

// Provider is the base Subagent provider contract. Every Provider supports
// one-shot Start; continuable preparation is an optional additional ability.
type Provider interface {
	Name() string
	Capabilities() Capabilities
	InheritsParentContext() bool
	Start(context.Context, ResolvedStartRequest) (Run, error)
}

// ContinuableProvider is a Provider that can additionally seed a continuable
// child. Preparation returns data only; Runtime owns the child lifecycle.
type ContinuableProvider interface {
	Provider
	PrepareContinuable(
		context.Context,
		ContinuableCreateRequest,
	) (ContinuableCreateSpec, error)
}

// ContinuableCreateRequest asks a Provider for detached creation data after
// Runtime has reserved the durable child identity.
type ContinuableCreateRequest struct {
	SessionID session.SessionID
	Parent    agent.Agent
}

// ContinuableCreateSpec is the Provider's detached contribution to creation.
type ContinuableCreateSpec struct {
	Seed []session.Event
}

// ProviderRegistration owns one exact Provider registration.
type ProviderRegistration interface {
	Unregister(context.Context) error
}

// ProviderRegistry owns Provider registration, exact lookup, and stable order.
type ProviderRegistry interface {
	RegisterProvider(context.Context, Provider) (ProviderRegistration, error)
	GetProvider(string) (Provider, bool)
	ListProviders() []string
}
