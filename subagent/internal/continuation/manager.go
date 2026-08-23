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
	Agents       agent.Registry
	Custody      agent.Custody
	Sessions     session.LiveStore
	Persistence  persistence.Persistence
	Providers    Providers
	Lifecycle    Lifecycle
	ScopeBuilder ScopeBuilder
	Failures     FailureReporter
}

// Manager owns every process-local continuable Activation.
type Manager struct {
	dependencies Dependencies
	residency    *residency
}

// New constructs a continuation Manager when Agent and Session ownership are
// available.
func New(dependencySet Dependencies) (*Manager, error) {
	if dependencySet.Agents == nil || dependencySet.Custody.IsZero() ||
		dependencySet.Sessions == nil ||
		dependencySet.Providers == nil ||
		dependencySet.ScopeBuilder == nil ||
		dependencySet.Failures == nil {
		return nil, errors.New(
			"subagent: continuation requires Agent Registry, Agent Custody, Session LiveStore, Providers, child Scope builder, and failure reporter",
		)
	}
	return &Manager{
		dependencies: dependencySet,
		residency:    newResidency(),
	}, nil
}

// MessageLeftInbox clears the waking admission guard for one message.
func (owner *Manager) MessageLeftInbox(
	childAgent agent.Agent,
	messageID llm.MessageID,
) {
	owner.residency.mutex.Lock()
	epoch := owner.residency.activations[childAgent.ID()]
	if epoch != nil && agent.Same(epoch.handle.Subject, childAgent) {
		delete(epoch.accepted, messageID)
		wake(epoch)
	}
	owner.residency.mutex.Unlock()
}

// AgentDisposed removes the admission cutoff retained for an externally
// disposed exact Agent. Manager-owned handles normally leave through Dispose.
func (owner *Manager) AgentDisposed(childAgent agent.Agent) {
	var externallyDisposed *Activation
	owner.residency.mutex.Lock()
	if closingRoot := owner.residency.closingRoots[childAgent.ID()]; agent.Same(closingRoot, childAgent) {
		delete(owner.residency.closingRoots, childAgent.ID())
	}
	epoch := owner.residency.activations[childAgent.ID()]
	if epoch != nil && agent.Same(epoch.handle.Subject, childAgent) &&
		epoch.disposal == nil {
		transaction := &disposal{
			done: make(chan struct{}),
		}
		epoch.disposal = transaction
		delete(owner.residency.activations, epoch.childID)
		externallyDisposed = epoch
	}
	owner.residency.mutex.Unlock()
	if externallyDisposed == nil {
		return
	}
	parentAgent, found := owner.dependencies.Agents.Get(
		externallyDisposed.parentID,
	)
	if found && owner.dependencies.Lifecycle != nil {
		owner.dependencies.Lifecycle.Ended(
			parentAgent,
			subagent.Ended{
				RunID:      externallyDisposed.runID,
				Provider:   externallyDisposed.providerName,
				ID:         externallyDisposed.childID,
				Local:      true,
				StopReason: subagent.StopAborted,
			},
		)
	}
	owner.residency.mutex.Lock()
	for _, candidate := range owner.residency.activations {
		if _, owned := candidate.ownedChildren[externallyDisposed.childID]; owned {
			delete(candidate.ownedChildren, externallyDisposed.childID)
			wake(candidate)
		}
	}
	close(externallyDisposed.disposal.done)
	owner.residency.mutex.Unlock()
}
