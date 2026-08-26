package continuation

import (
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/session/persistence"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/subagent/internal/childscope"
)

// Providers resolves the exact currently registered Provider.
type Providers interface {
	GetProvider(string) (subagent.Provider, bool)
}

// Lifecycle publishes one Activation epoch's paired facts.
type Lifecycle interface {
	Started(agent.Agent, subagent.Started)
	Ended(agent.Agent, subagent.Ended)
}

// ScopeBuilder builds the Provisioner for one continuable child Scope.
// Continuation owns when provisioning happens; childscope owns what is
// installed into the unpublished Agent.
type ScopeBuilder interface {
	Provisioner(childscope.ContinuableInput) agent.Provisioner
}

// Dependencies are the owner-supplied capabilities used by Manager.
type Dependencies struct {
	Agents      agent.Registry
	Constructor agent.Constructor
	Descendants agent.RuntimeDescendants
	Sessions    session.LiveStore
	Persistence persistence.Persistence
	Providers   Providers
	Lifecycle   Lifecycle
	Scopes      ScopeBuilder
	Failures    FailureReporter
}

// Manager owns every process-local continuable Activation.
type Manager struct {
	dependencies Dependencies
	activations  *activationRegistry
}

// New constructs a continuation Manager when Agent and Session ownership are
// available.
func New(dependencySet Dependencies) (*Manager, error) {
	if dependencySet.Agents == nil || dependencySet.Constructor == nil ||
		dependencySet.Descendants == nil || dependencySet.Sessions == nil ||
		dependencySet.Providers == nil ||
		dependencySet.Scopes == nil ||
		dependencySet.Failures == nil {
		return nil, errors.New(
			"subagent: continuation requires Agent Registry, Constructor, runtime descendant observation, Session LiveStore, Providers, child Scope builder, and failure reporter",
		)
	}
	return &Manager{
		dependencies: dependencySet,
		activations:  newActivationRegistry(),
	}, nil
}

// MessageLeftInbox clears the waking admission guard for one message.
func (owner *Manager) MessageLeftInbox(
	childAgent agent.Agent,
	messageID llm.MessageID,
) {
	owner.activations.mutex.Lock()
	epoch := owner.activations.activations[childAgent.ID()]
	if epoch != nil && agent.Same(epoch.handle.Subject, childAgent) {
		delete(epoch.accepted, messageID)
		wake(epoch)
	}
	owner.activations.mutex.Unlock()
}

// AgentDisposed removes the admission cutoff retained for an externally
// disposed exact Agent. Manager-owned handles normally leave through Dispose.
func (owner *Manager) AgentDisposed(childAgent agent.Agent) {
	var externallyDisposed *Activation
	owner.activations.mutex.Lock()
	epoch := owner.activations.activations[childAgent.ID()]
	if epoch != nil && agent.Same(epoch.handle.Subject, childAgent) &&
		epoch.disposal == nil {
		transaction := &disposal{
			done: make(chan struct{}),
		}
		epoch.disposal = transaction
		delete(owner.activations.activations, epoch.childID)
		externallyDisposed = epoch
	}
	parentID := childAgent.SessionValue().Header().ParentSession
	if parentID != nil {
		if parentEpoch := owner.activations.activations[*parentID]; parentEpoch != nil {
			wake(parentEpoch)
		}
	}
	owner.activations.mutex.Unlock()
	if externallyDisposed == nil {
		return
	}
	if externallyDisposed.parent != nil && owner.dependencies.Lifecycle != nil {
		owner.dependencies.Lifecycle.Ended(
			externallyDisposed.parent,
			subagent.Ended{
				RunID:      externallyDisposed.runID,
				Provider:   externallyDisposed.providerName,
				ID:         externallyDisposed.childID,
				Local:      true,
				StopReason: subagent.StopAborted,
			},
		)
	}
	owner.activations.mutex.Lock()
	close(externallyDisposed.disposal.done)
	owner.activations.mutex.Unlock()
}
