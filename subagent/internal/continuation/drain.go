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

// DrainChildren releases selected resident direct children.
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
		if epoch.parentID != parentAgent.ID() ||
			!containsAgent(epoch.ancestry, parentAgent) {
			owner.residency.mutex.Unlock()
			return unauthorized(
				fmt.Sprintf("subagent %q is not a direct child of Agent %q", childID, parentAgent.ID()),
			)
		}
		targets = append(targets, epoch)
	}
	owner.residency.mutex.Unlock()
	return owner.disposeAll(requestContext, targets)
}

// DrainDescendants releases every resident strict descendant of exact roots.
func (owner *Manager) DrainDescendants(
	requestContext context.Context,
	parents []agent.Agent,
) error {
	liveParents := make([]agent.Agent, 0, len(parents))
	for _, parentAgent := range parents {
		if parentAgent != nil && owner.dependencies.Agents.Contains(parentAgent) {
			liveParents = append(liveParents, parentAgent)
		}
	}
	if contextErr := checkContext(requestContext, "descendant drain"); contextErr != nil {
		return contextErr
	}
	owner.residency.mutex.Lock()
	for _, parentAgent := range liveParents {
		owner.residency.closingRoots[parentAgent.ID()] = parentAgent
	}
	waits := make([]chan struct{}, 0)
	for admission := range owner.residency.building {
		if lineageContainsAny(admission.lineage, liveParents) {
			waits = append(waits, admission.done)
		}
	}
	owner.residency.mutex.Unlock()
	for _, done := range waits {
		select {
		case <-requestContext.Done():
			return requestContext.Err()
		case <-done:
		}
	}
	targets := make([]*Activation, 0)
	owner.residency.mutex.Lock()
	for _, epoch := range owner.residency.activations {
		for _, parentAgent := range liveParents {
			if !agent.Same(epoch.handle.Subject, parentAgent) &&
				containsAgent(epoch.ancestry, parentAgent) {
				targets = append(targets, epoch)
				break
			}
		}
	}
	owner.residency.mutex.Unlock()
	return owner.disposeAll(requestContext, rootsOf(targets))
}

// Drain closes manager-wide admission and releases the resident forest.
func (owner *Manager) Drain(requestContext context.Context) error {
	if contextErr := checkContext(requestContext, "continuation drain"); contextErr != nil {
		return contextErr
	}
	owner.residency.mutex.Lock()
	owner.residency.draining = true
	waits := make([]chan struct{}, 0, len(owner.residency.building))
	for admission := range owner.residency.building {
		waits = append(waits, admission.done)
	}
	owner.residency.mutex.Unlock()
	for _, done := range waits {
		select {
		case <-requestContext.Done():
			return requestContext.Err()
		case <-done:
		}
	}
	owner.residency.mutex.Lock()
	all := make([]*Activation, 0, len(owner.residency.activations))
	for _, epoch := range owner.residency.activations {
		all = append(all, epoch)
	}
	owner.residency.mutex.Unlock()
	return owner.disposeAll(requestContext, rootsOf(all))
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
	lineage := owner.currentLineage(parentAgent)
	owner.residency.mutex.Lock()
	closing := owner.closingForLocked(lineage)
	owner.residency.mutex.Unlock()
	if closing {
		return &subagent.Error{
			Code:    subagent.ErrorDraining,
			Message: "continuable subagents are draining; the operation was not admitted",
		}
	}
	return nil
}

func (owner *Manager) beginMaterialization(
	parentAgent agent.Agent,
) (*materialization, error) {
	lineage := owner.currentLineage(parentAgent)
	owner.residency.mutex.Lock()
	defer owner.residency.mutex.Unlock()
	if owner.closingForLocked(lineage) {
		return nil, &subagent.Error{
			Code:    subagent.ErrorDraining,
			Message: "continuable subagents are draining; materialization was not admitted",
		}
	}
	admission := &materialization{
		lineage: lineage,
		done:    make(chan struct{}),
	}
	owner.residency.building[admission] = struct{}{}
	return admission, nil
}

func (owner *Manager) finishMaterialization(admission *materialization) {
	owner.residency.mutex.Lock()
	if _, found := owner.residency.building[admission]; found {
		delete(owner.residency.building, admission)
		close(admission.done)
	}
	owner.residency.mutex.Unlock()
}

func (owner *Manager) closingForLocked(lineage []agent.Agent) bool {
	if owner.residency.draining {
		return true
	}
	for _, member := range lineage {
		if closing := owner.residency.closingRoots[member.ID()]; agent.Same(closing, member) {
			return true
		}
	}
	return false
}

func (owner *Manager) currentLineage(parentAgent agent.Agent) []agent.Agent {
	members := []agent.Agent{parentAgent}
	seen := map[session.SessionID]struct{}{
		parentAgent.ID(): {},
	}
	parentID := parentAgent.SessionValue().Header().ParentSession
	for parentID != nil {
		ancestor, found := owner.dependencies.Agents.Get(*parentID)
		if !found {
			break
		}
		if _, duplicate := seen[ancestor.ID()]; duplicate {
			break
		}
		seen[ancestor.ID()] = struct{}{}
		members = append(members, ancestor)
		parentID = ancestor.SessionValue().Header().ParentSession
	}
	return members
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

func (owner *Manager) buildLineage(parentAgent agent.Agent, childAgent agent.Agent) []agent.Agent {
	members := []agent.Agent{childAgent, parentAgent}
	seen := map[session.SessionID]struct{}{
		childAgent.ID():  {},
		parentAgent.ID(): {},
	}
	parentID := parentAgent.SessionValue().Header().ParentSession
	for parentID != nil {
		ancestor, found := owner.dependencies.Agents.Get(*parentID)
		if !found {
			break
		}
		if _, duplicate := seen[ancestor.ID()]; duplicate {
			break
		}
		seen[ancestor.ID()] = struct{}{}
		members = append(members, ancestor)
		parentID = ancestor.SessionValue().Header().ParentSession
	}
	return members
}

func rootsOf(candidates []*Activation) []*Activation {
	owned := make(map[session.SessionID]struct{})
	for _, epoch := range candidates {
		for childID := range epoch.ownedChildren {
			owned[childID] = struct{}{}
		}
	}
	rootEpochs := make([]*Activation, 0, len(candidates))
	for _, epoch := range candidates {
		if _, isOwned := owned[epoch.childID]; !isOwned {
			rootEpochs = append(rootEpochs, epoch)
		}
	}
	return rootEpochs
}

func containsAgent(members []agent.Agent, candidate agent.Agent) bool {
	for _, member := range members {
		if agent.Same(member, candidate) {
			return true
		}
	}
	return false
}

func lineageContainsAny(members []agent.Agent, candidates []agent.Agent) bool {
	for _, candidate := range candidates {
		if containsAgent(members, candidate) {
			return true
		}
	}
	return false
}
