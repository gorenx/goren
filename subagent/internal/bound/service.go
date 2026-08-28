// Package bound owns durable parent bindings, Bound config revisions, and
// resident Bound Agent epochs.
package bound

import (
	"context"
	"errors"
	"fmt"

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

// InteractionFailure identifies one contained parent-interaction delivery or
// recovery failure. The durable cursor remains unchanged on failure.
type InteractionFailure struct {
	ParentID session.SessionID
	ChildID  session.SessionID
	Error    error
}

// FailureReporter receives Bound failures that must not veto parent Agent
// publication.
type FailureReporter interface {
	ReportBoundMaterializationFailure(MaterializationFailure)
	ReportBoundFinalFlushFailure(FinalFlushFailure)
	ReportBoundInteractionFailure(InteractionFailure)
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

// Service orchestrates Bound use cases through the materialization policy,
// binding state slots, and interaction delivery supervisor.
type Service struct {
	dependencies Dependencies
	materializer materializer
	bindings     *bindingSlots
	residents    *residentExecutions
	interactions *deliverySupervisor
}

// New constructs the Bound mode service.
func New(dependencySet Dependencies) (*Service, error) {
	if dependencySet.Extensions == nil {
		return nil, errors.New(
			"subagent: Bound requires Extension selection",
		)
	}
	bindings := newBindingSlots()
	interactions := newDeliverySupervisor(dependencySet)
	return &Service{
		dependencies: dependencySet,
		materializer: materializer{
			constructor:      dependencySet.Constructor,
			persistence:      dependencySet.Persistence,
			seedBuilders:     dependencySet.SeedBuilders,
			delegation:       dependencySet.Delegation,
			commonExtensions: dependencySet.CommonExtensions,
			extensions:       dependencySet.Extensions,
		},
		bindings:     bindings,
		residents:    newResidentExecutions(dependencySet, bindings),
		interactions: interactions,
	}, nil
}

// Mode identifies the business mode implemented by Service.
func (*Service) Mode() subagent.Mode {
	return subagent.ModeBound
}

// SessionEventAppended wakes existing interaction deliveries after a complete
// parent turn has been committed.
func (owner *Service) SessionEventAppended(fact session.EventAppended) {
	if owner == nil {
		return
	}
	owner.interactions.sessionEventAppended(fact)
}

// AgentDisposed stops deliveries owned by the exact parent Agent epoch.
func (owner *Service) AgentDisposed(
	_ context.Context,
	subject agent.Agent,
) error {
	if owner == nil {
		return nil
	}
	owner.interactions.agentDisposed(subject)
	return nil
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
