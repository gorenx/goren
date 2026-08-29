// Package bound owns global Definitions, per-user-Session Bindings, resident
// Bound child epochs, and Inbox delivery.
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
	boundcontract "github.com/gorenx/goren/subagent/bound"
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

// ReconcileFailure identifies one contained user Session consistency failure.
type ReconcileFailure struct {
	ParentID session.SessionID
	Error    error
}

// FailureReporter receives Bound failures that must not veto parent Agent
// publication or a committed global Definition.
type FailureReporter interface {
	ReportBoundMaterializationFailure(MaterializationFailure)
	ReportBoundFinalFlushFailure(FinalFlushFailure)
	ReportBoundReconcileFailure(ReconcileFailure)
}

// Extensions is Bound's consumer-owned view of named child Extensions.
type Extensions interface {
	Provision([]string) (agent.Provisioner, error)
}

// Dependencies contains the capabilities required by Bound ownership.
type Dependencies struct {
	Agents           agent.Registry
	Constructor      agent.Constructor
	Sessions         session.LiveStore
	Persistence      persistence.Persistence
	Projections      sessionprojection.Registry
	Definitions      DefinitionStore
	Delegation       approval.DelegationPolicy
	CommonExtensions agent.Provisioner
	Extensions       Extensions
	Publisher        sharedexecution.EventPublisher
	Executions       *sharedexecution.Registry
	Failures         FailureReporter
}

// Service owns global Definition management and every live Binding worker.
type Service struct {
	dependencies Dependencies
	definitions  *definitionCatalog
	workers      *registry
	scheduler    *reconcileScheduler
}

// New loads the committed Definition index and starts no background work.
func New(
	requestContext context.Context,
	dependencySet Dependencies,
) (*Service, error) {
	if requestContext == nil {
		return nil, errors.New("subagent: Bound New Context is nil")
	}
	if dependencySet.Agents == nil || dependencySet.Constructor == nil ||
		dependencySet.Sessions == nil || dependencySet.Persistence == nil ||
		dependencySet.Projections == nil || dependencySet.Executions == nil {
		return nil, errors.New(
			"subagent: Bound requires Agent, Session, Persistence, " +
				"Projection, and Execution capabilities",
		)
	}
	if dependencySet.Extensions == nil {
		return nil, errors.New(
			"subagent: Bound requires Extension selection",
		)
	}
	if dependencySet.Definitions == nil {
		return nil, errors.New(
			"subagent: Bound requires a Definition Store",
		)
	}
	materializerValue := &materializer{
		constructor:      dependencySet.Constructor,
		persistence:      dependencySet.Persistence,
		delegation:       dependencySet.Delegation,
		commonExtensions: dependencySet.CommonExtensions,
		extensions:       dependencySet.Extensions,
	}
	owner := &Service{
		dependencies: dependencySet,
	}
	owner.workers = newRegistry(
		dependencySet,
		materializerValue,
	)
	owner.scheduler = newReconcileScheduler(owner)
	definitions, err := newDefinitionCatalog(
		requestContext,
		dependencySet.Definitions,
		owner.scheduler,
	)
	if err != nil {
		_ = owner.scheduler.close(context.WithoutCancel(requestContext))
		return nil, err
	}
	owner.definitions = definitions
	owner.workers.definitions = definitions
	return owner, nil
}

// Mode identifies the business mode implemented by Service.
func (*Service) Mode() subagent.Mode {
	return subagent.ModeBound
}

// List returns the detached committed global Definition index.
func (owner *Service) List(
	requestContext context.Context,
) ([]boundcontract.Definition, error) {
	return owner.definitions.List(requestContext)
}

// Create commits one new global Definition before requesting live reconcile.
func (owner *Service) Create(
	requestContext context.Context,
	creation boundcontract.Creation,
) (boundcontract.Definition, error) {
	return owner.definitions.Create(requestContext, creation)
}

// Replace commits one complete next global Definition revision.
func (owner *Service) Replace(
	requestContext context.Context,
	replacement boundcontract.Replacement,
) (boundcontract.Definition, error) {
	return owner.definitions.Replace(requestContext, replacement)
}

// SessionStarted requests a level-triggered consistency pass for one exact
// user Agent. Child Sessions are filtered before any task is admitted.
func (owner *Service) SessionStarted(parentAgent agent.Agent) {
	owner.scheduler.request(parentAgent)
}

// AgentDisposed releases live routing for the exact parent Agent epoch.
func (owner *Service) AgentDisposed(
	requestContext context.Context,
	subject agent.Agent,
) error {
	if owner == nil {
		return nil
	}
	owner.scheduler.agentDisposed(subject)
	return owner.workers.agentDisposed(requestContext, subject)
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
	if !isUserAgent(parentAgent) {
		return &subagent.Error{
			Code: subagent.ErrorUnauthorized,
			Message: fmt.Sprintf(
				"Bound operation requires direct user Session %q",
				parentAgent.ID(),
			),
		}
	}
	return nil
}

func isUserAgent(subject agent.Agent) bool {
	if subject == nil || subject.SessionValue() == nil {
		return false
	}
	header := subject.SessionValue().Header()
	return header.Origin == "" && header.ParentSession == nil
}

func checkContext(requestContext context.Context, operationName string) error {
	if requestContext == nil {
		return errors.New("subagent: " + operationName + " Context is nil")
	}
	return context.Cause(requestContext)
}

func unavailableDependency(dependencyName string) error {
	return fmt.Errorf("subagent: Bound %s is unavailable", dependencyName)
}

// Close stops scheduler admission, waits managed tasks, closes workers in
// parallel, then releases the independent Definition Store last.
func (owner *Service) Close(closeContext context.Context) error {
	if closeContext == nil {
		closeContext = context.Background()
	}
	schedulerErr := owner.scheduler.close(closeContext)
	workerErr := owner.workers.close(closeContext)
	storeErr := owner.definitions.Close(closeContext)
	return errors.Join(schedulerErr, workerErr, storeErr)
}

var _ boundcontract.Definitions = (*Service)(nil)
