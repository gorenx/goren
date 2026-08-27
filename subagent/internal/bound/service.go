// Package bound owns durable parent bindings, Bound config revisions, and
// resident Bound Agent epochs.
package bound

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/approval"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/session/persistence"
	sessionprojection "github.com/gorenx/goren/session/projection"
	"github.com/gorenx/goren/subagent"
	sharedexecution "github.com/gorenx/goren/subagent/internal/execution"
)

// MaterializationFailure identifies a contained Bound create or restore
// failure. The durable parent event records only the stable result class.
type MaterializationFailure struct {
	ParentID session.SessionID
	ChildID  session.SessionID
	Error    error
}

// FinalFlushFailure identifies a contained durability failure while a Bound
// Agent epoch is already terminating.
type FinalFlushFailure struct {
	ChildID session.SessionID
	Error   error
}

// FailureReporter receives Bound failures that must not veto parent Agent
// publication.
type FailureReporter interface {
	ReportBoundMaterializationFailure(MaterializationFailure)
	ReportBoundFinalFlushFailure(FinalFlushFailure)
}

// Extensions is Bound's consumer-owned view of named child Extensions.
// Validation happens before config durability; Provision builds the exact
// epoch installation transaction without exposing the provider Registry.
type Extensions interface {
	Validate([]string) error
	Provision([]string) (agent.Provisioner, error)
}

// Dependencies contains the capabilities required by Bound config and
// resident Agent epoch management.
type Dependencies struct {
	Agents           agent.Registry
	Constructor      agent.Constructor
	Sessions         session.LiveStore
	Persistence      persistence.Persistence
	Projections      sessionprojection.Registry
	SeedBuilders     subagent.SeedBuilderRegistry
	Delegation       approval.DelegationPolicy
	CommonExtensions agent.Provisioner
	Extensions       Extensions
	Publisher        sharedexecution.EventPublisher
	Executions       *sharedexecution.Registry
	Failures         FailureReporter
}

type operationKey struct {
	parentID session.SessionID
	childID  session.SessionID
}

type operation struct {
	mutex        sync.Mutex
	currentMutex sync.Mutex
	current      *currentExecution
}

func (owner *operation) loadCurrent() *currentExecution {
	owner.currentMutex.Lock()
	defer owner.currentMutex.Unlock()
	return owner.current
}

func (owner *operation) storeCurrent(current *currentExecution) {
	owner.currentMutex.Lock()
	owner.current = current
	owner.currentMutex.Unlock()
}

func (owner *operation) clearCurrent(expected *currentExecution) {
	owner.currentMutex.Lock()
	if owner.current == expected {
		owner.current = nil
	}
	owner.currentMutex.Unlock()
}

// Service owns Bound use cases. One parent lock serializes title allocation;
// one parent-child lock serializes config replacement, epoch replacement, and
// message admission for that exact binding.
type Service struct {
	dependencies Dependencies
	mutex        sync.Mutex
	operations   map[operationKey]*operation
	parents      map[session.SessionID]*operation
}

// New constructs the Bound mode service.
func New(dependencySet Dependencies) (*Service, error) {
	if dependencySet.Extensions == nil {
		return nil, errors.New(
			"subagent: Bound requires Extension selection",
		)
	}
	return &Service{
		dependencies: dependencySet,
		operations:   make(map[operationKey]*operation),
		parents:      make(map[session.SessionID]*operation),
	}, nil
}

// Mode identifies the business mode implemented by Service.
func (*Service) Mode() subagent.Mode {
	return subagent.ModeBound
}

func (owner *Service) childOperation(
	parentID session.SessionID,
	childID session.SessionID,
) *operation {
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	key := operationKey{
		parentID: parentID,
		childID:  childID,
	}
	current := owner.operations[key]
	if current == nil {
		current = &operation{}
		owner.operations[key] = current
	}
	return current
}

func (owner *Service) parentOperation(
	parentID session.SessionID,
) *operation {
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	current := owner.parents[parentID]
	if current == nil {
		current = &operation{}
		owner.parents[parentID] = current
	}
	return current
}

func (owner *Service) authorizeParent(parentAgent agent.Agent) error {
	if owner == nil || owner.dependencies.Agents == nil || parentAgent == nil ||
		!owner.dependencies.Agents.Contains(parentAgent) {
		parentID := session.SessionID("")
		if parentAgent != nil {
			parentID = parentAgent.ID()
		}
		return &subagent.Error{
			Code: subagent.ErrorUnauthorized,
			Message: fmt.Sprintf(
				"Bound operation requires exact live parent Agent %q",
				parentID,
			),
		}
	}
	if parentAgent.SessionValue() == nil {
		return errors.New("subagent: Bound parent Session is unavailable")
	}
	return nil
}

func checkContext(ctx context.Context, operationName string) error {
	if ctx == nil {
		return errors.New("subagent: " + operationName + " context is nil")
	}
	return context.Cause(ctx)
}

func unavailableDependency(dependencyName string) error {
	return fmt.Errorf("subagent: Bound %s is unavailable", dependencyName)
}
