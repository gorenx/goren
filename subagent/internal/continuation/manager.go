package continuation

import (
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/session/persistence"
	"github.com/gorenx/goren/subagent"
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

// Composer builds one fresh Agent Provisioner for a materializing Activation.
// Continuation owns when composition happens; the collaborator owns what the
// child Scope contains.
type Composer interface {
	Compose(Composition) agent.Provisioner
}

// Composition is the immutable child identity supplied to Composer.
type Composition struct {
	ChildID    session.SessionID
	ParentID   session.SessionID
	Descriptor subagent.ContinuableDescriptor
	Fresh      bool
}

// Dependencies are the owner-supplied capabilities used by Manager.
type Dependencies struct {
	Agents      agent.Registry
	Sessions    session.LiveStore
	Persistence persistence.Persistence
	Providers   Providers
	Lifecycle   Lifecycle
	Composer    Composer
}

// Manager owns every process-local continuable Activation.
type Manager struct {
	dependencies Dependencies
	residency    *residency
}

// New constructs a continuation Manager when Agent and Session ownership are
// available.
func New(dependencySet Dependencies) (*Manager, error) {
	if dependencySet.Agents == nil || dependencySet.Sessions == nil ||
		dependencySet.Providers == nil || dependencySet.Composer == nil {
		return nil, errors.New(
			"subagent: continuation requires Agent Registry, Session LiveStore, Providers, and Composer",
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
		!epoch.closing {
		epoch.closing = true
		epoch.terminalReason = subagent.StopAborted
		epoch.disposeDone = make(chan struct{})
		delete(owner.residency.activations, epoch.childID)
		for _, candidate := range owner.residency.activations {
			if _, owned := candidate.ownedChildren[epoch.childID]; owned {
				delete(candidate.ownedChildren, epoch.childID)
				wake(candidate)
			}
		}
		close(epoch.disposeDone)
		externallyDisposed = epoch
	}
	owner.residency.mutex.Unlock()
	if externallyDisposed == nil || owner.dependencies.Lifecycle == nil {
		return
	}
	parentAgent, found := owner.dependencies.Agents.Get(
		externallyDisposed.parentID,
	)
	if !found {
		return
	}
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
