// Package continuable owns resumable Subagent Session identity, exact
// execution materialization, messaging, and settlement policy.
package continuable

import (
	"context"
	"errors"
	"sync"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/approval"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/session/persistence"
	"github.com/gorenx/goren/subagent"
	sharedexecution "github.com/gorenx/goren/subagent/internal/execution"
)

// SeedBuilders resolves the exact registered child seed strategy.
type SeedBuilders interface {
	Find(string) (subagent.SeedBuilder, bool)
}

// FinalFlushFailure identifies a contained durability failure.
type FinalFlushFailure struct {
	ChildID session.SessionID
	Error   error
}

// FailureReporter receives failures that cannot change lifecycle completion.
type FailureReporter interface {
	ReportFinalFlushFailure(FinalFlushFailure)
}

// Dependencies contains the capabilities required by Continuable execution.
type Dependencies struct {
	Agents       agent.Registry
	Constructor  agent.Constructor
	Descendants  agent.RuntimeDescendants
	Sessions     session.LiveStore
	Persistence  persistence.Persistence
	SeedBuilders SeedBuilders
	Publisher    sharedexecution.EventPublisher
	Delegation   approval.DelegationPolicy
	Extensions   agent.Provisioner
	Failures     FailureReporter
	Executions   *sharedexecution.Registry
}

// Service owns Continuable behavior and the per-child serialization index.
// Module admission belongs to subagents.Service; Execution phase belongs to
// the shared execution object.
type Service struct {
	dependencies Dependencies
	mutex        sync.Mutex
	slots        map[session.SessionID]*childSlot
}

// Mode identifies the business mode implemented by Service.
func (*Service) Mode() subagent.Mode {
	return subagent.ModeContinuable
}

type childSlot struct {
	mutex   sync.Mutex
	users   int
	current *currentExecution
}

type currentExecution struct {
	running    *sharedexecution.Execution
	terminator *executionTerminator
	slot       *childSlot
	wake       chan struct{}
}

// New constructs an accepting Continuable Service.
func New(dependencySet Dependencies) (*Service, error) {
	if dependencySet.Agents == nil || dependencySet.Constructor == nil ||
		dependencySet.Descendants == nil || dependencySet.Sessions == nil ||
		dependencySet.Persistence == nil || dependencySet.SeedBuilders == nil ||
		dependencySet.Failures == nil ||
		dependencySet.Executions == nil {
		return nil, errors.New(
			"subagent: Continuable requires Agent Registry, Constructor, " +
				"runtime descendants, Session LiveStore, persistence, " +
				"SeedBuilders, failure reporter, and " +
				"Execution Registry",
		)
	}
	return &Service{
		dependencies: dependencySet,
		slots:        make(map[session.SessionID]*childSlot),
	}, nil
}

// Close rejects new work, requests every current Execution to stop, and waits
// only until each exact Agent enters Closing. Agent owns Scope teardown.
func (owner *Service) Close(closeContext context.Context) error {
	if closeContext == nil {
		closeContext = context.Background()
	}
	owner.mutex.Lock()
	slots := make([]*childSlot, 0, len(owner.slots))
	for _, slot := range owner.slots {
		slots = append(slots, slot)
	}
	owner.mutex.Unlock()
	targets := make([]*currentExecution, 0, len(slots))
	for _, slot := range slots {
		slot.mutex.Lock()
		if slot.current != nil {
			targets = append(targets, slot.current)
		}
		slot.mutex.Unlock()
	}
	for _, current := range targets {
		current.running.Stop(sharedexecution.StopModule)
	}
	for _, current := range targets {
		select {
		case <-current.terminator.handle.ClosingSignal():
		case <-closeContext.Done():
			return context.Cause(closeContext)
		}
	}
	return nil
}

func (owner *Service) authorizeParent(parentAgent agent.Agent) error {
	if parentAgent == nil || !owner.dependencies.Agents.Contains(parentAgent) {
		return unauthorized(
			"Continuable operation requires the exact live parent Agent",
		)
	}
	return nil
}

func (owner *Service) acquireSlot(childID session.SessionID) *childSlot {
	owner.mutex.Lock()
	slot := owner.slots[childID]
	if slot == nil {
		slot = &childSlot{}
		owner.slots[childID] = slot
	}
	slot.users++
	owner.mutex.Unlock()
	return slot
}

func (owner *Service) releaseSlot(
	childID session.SessionID,
	slot *childSlot,
) {
	owner.mutex.Lock()
	slot.users--
	owner.mutex.Unlock()
	owner.removeUnusedSlot(childID, slot)
}

func (owner *Service) detach(
	childID session.SessionID,
	current *currentExecution,
) {
	slot := current.slot
	slot.mutex.Lock()
	if slot.current == current {
		slot.current = nil
	}
	slot.mutex.Unlock()
	owner.removeUnusedSlot(childID, slot)
}

func (owner *Service) removeUnusedSlot(
	childID session.SessionID,
	slot *childSlot,
) {
	owner.mutex.Lock()
	slot.mutex.Lock()
	unused := slot.current == nil
	slot.mutex.Unlock()
	if owner.slots[childID] == slot && slot.users == 0 && unused {
		delete(owner.slots, childID)
	}
	owner.mutex.Unlock()
}

func unauthorized(message string) error {
	return &subagent.Error{
		Code:    subagent.ErrorUnauthorized,
		Message: message,
	}
}
