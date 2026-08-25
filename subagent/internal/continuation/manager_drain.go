package continuation

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
)

// DrainChildren releases selected resident direct children. The Manager owns
// authorization and settlement; the Agent Registry owns descendant ordering.
func (owner *Manager) DrainChildren(
	requestContext context.Context,
	parentAgent agent.Agent,
	childIDs []session.SessionID,
) error {
	if contextErr := checkContext(requestContext, "selected child drain"); contextErr != nil {
		return contextErr
	}
	if !owner.dependencies.Agents.Contains(parentAgent) {
		return unauthorized("selected child teardown requires the exact live parent Agent")
	}
	targets := make([]*Activation, 0, len(childIDs))
	seen := make(map[session.SessionID]struct{}, len(childIDs))
	owner.residency.mutex.Lock()
	for _, childID := range childIDs {
		if _, duplicate := seen[childID]; duplicate {
			continue
		}
		seen[childID] = struct{}{}
		epoch := owner.residency.activations[childID]
		if epoch == nil {
			continue
		}
		if epoch.parentID != parentAgent.ID() {
			owner.residency.mutex.Unlock()
			return unauthorized(
				fmt.Sprintf(
					"subagent %q is not a direct child of Agent %q",
					childID,
					parentAgent.ID(),
				),
			)
		}
		targets = append(targets, epoch)
	}
	owner.residency.mutex.Unlock()
	return owner.disposeAll(requestContext, targets)
}

// DrainDescendants closes the complete managed runtime descendant sets below
// exact authorized roots. The Coordinator installs the admission cutoff and
// joins in-flight construction before returning.
func (owner *Manager) DrainDescendants(
	requestContext context.Context,
	parents []agent.Agent,
) error {
	if contextErr := checkContext(requestContext, "descendant drain"); contextErr != nil {
		return contextErr
	}
	var closeErr error
	for _, parentAgent := range parents {
		if parentAgent == nil || !owner.dependencies.Agents.Contains(parentAgent) {
			continue
		}
		closeErr = errors.Join(
			closeErr,
			owner.dependencies.Descendants.CloseDescendants(
				requestContext,
				parentAgent,
			),
		)
	}
	return closeErr
}

// Drain closes Subagent admission, installs descendant admission cutoffs on
// every ordinary live Agent, and then releases any orphaned resident epochs.
func (owner *Manager) Drain(requestContext context.Context) error {
	if contextErr := checkContext(requestContext, "continuation drain"); contextErr != nil {
		return contextErr
	}
	owner.residency.mutex.Lock()
	owner.residency.draining = true
	owner.residency.mutex.Unlock()

	var closeErr error
	for _, subject := range owner.dependencies.Agents.List() {
		if subject == nil ||
			subject.SessionValue().Header().Origin == session.OriginSubagent {
			continue
		}
		closeErr = errors.Join(
			closeErr,
			owner.dependencies.Descendants.CloseDescendants(
				requestContext,
				subject,
			),
		)
	}

	owner.residency.mutex.Lock()
	remaining := make([]*Activation, 0, len(owner.residency.activations))
	for _, epoch := range owner.residency.activations {
		remaining = append(remaining, epoch)
	}
	owner.residency.mutex.Unlock()
	return errors.Join(
		closeErr,
		owner.disposeAll(requestContext, activationRoots(remaining)),
	)
}

func (owner *Manager) disposeAll(
	requestContext context.Context,
	targets []*Activation,
) error {
	failures := make([]error, 0)
	for _, epoch := range targets {
		if disposeErr := owner.dispose(
			requestContext,
			epoch,
			subagent.StopAborted,
		); disposeErr != nil {
			failures = append(failures, disposeErr)
		}
	}
	if len(failures) == 0 {
		return nil
	}
	return &subagent.Error{
		Code:    subagent.ErrorActivationTeardownFailed,
		Message: fmt.Sprintf("continuable teardown failed at %d boundary(s)", len(failures)),
		Cause:   errors.Join(failures...),
	}
}

func (owner *Manager) assertAvailable(
	requestContext context.Context,
	childID session.SessionID,
	checkPersistence bool,
) error {
	if _, found := owner.dependencies.Agents.Get(childID); found {
		return duplicateChild(childID)
	}
	if _, found := owner.dependencies.Sessions.Get(childID); found {
		return duplicateChild(childID)
	}
	if checkPersistence {
		snapshots, listErr := owner.dependencies.Persistence.ListSnapshots(requestContext)
		if listErr != nil {
			return listErr
		}
		for _, snapshot := range snapshots {
			if snapshot.Header.ID == childID {
				return duplicateChild(childID)
			}
		}
	}
	return nil
}

func (owner *Manager) assertAdmitting(parentAgent agent.Agent) error {
	if parentAgent == nil || !owner.dependencies.Agents.Contains(parentAgent) {
		return unauthorized("continuable operation requires the exact live parent Agent")
	}
	owner.residency.mutex.Lock()
	draining := owner.residency.draining
	owner.residency.mutex.Unlock()
	if draining {
		return &subagent.Error{
			Code:    subagent.ErrorDraining,
			Message: "continuable subagents are draining; the operation was not admitted",
		}
	}
	return nil
}

func (owner *Manager) lockFor(childID session.SessionID) *sync.Mutex {
	owner.residency.mutex.Lock()
	defer owner.residency.mutex.Unlock()
	childMutex := owner.residency.locks[childID]
	if childMutex == nil {
		childMutex = &sync.Mutex{}
		owner.residency.locks[childID] = childMutex
	}
	return childMutex
}

func activationRoots(candidates []*Activation) []*Activation {
	identities := make(map[session.SessionID]struct{}, len(candidates))
	for _, epoch := range candidates {
		identities[epoch.childID] = struct{}{}
	}
	rootActivations := make([]*Activation, 0, len(candidates))
	for _, epoch := range candidates {
		if _, parentIsCandidate := identities[epoch.parentID]; !parentIsCandidate {
			rootActivations = append(rootActivations, epoch)
		}
	}
	return rootActivations
}
