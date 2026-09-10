// Package bound owns global definitions and durable per-parent bindings.
package bound

import (
	"context"
	"errors"
	"fmt"
	"sync"

	agentregistry "github.com/gorenx/goren/agent/registry"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/session/persistence"
	"github.com/gorenx/goren/subagent"
	boundcontract "github.com/gorenx/goren/subagent/bound"
	"github.com/gorenx/goren/subagent/internal/agentclient"
	sharedexecution "github.com/gorenx/goren/subagent/internal/execution"
	subagentprojection "github.com/gorenx/goren/subagent/internal/projection"
)

type MaterializationFailure struct {
	ParentID, ChildID session.SessionID
	Error             error
}
type FinalFlushFailure struct {
	ChildID session.SessionID
	Error   error
}
type ReconcileFailure struct {
	ParentID session.SessionID
	Error    error
}
type FailureReporter interface {
	ReportBoundMaterializationFailure(MaterializationFailure)
	ReportBoundFinalFlushFailure(FinalFlushFailure)
	ReportBoundReconcileFailure(ReconcileFailure)
}
type Agents interface {
	Find(context.Context, session.SessionID) (agentclient.Agent, error)
	CreateChild(context.Context, agentregistry.CreateChildAgent) (agentclient.Agent, error)
}
type History interface {
	Load(context.Context, session.SessionID) (*persistence.Log, error)
}
type Extensions interface {
	ConfigureCommon(context.Context, *subagent.ChildConfiguration) error
	ConfigureSelected(context.Context, []string, *subagent.ChildConfiguration) error
}
type Dependencies struct {
	Agents      Agents
	History     History
	Definitions DefinitionStore
	Extensions  Extensions
	Publisher   sharedexecution.EventPublisher
	Executions  *sharedexecution.Registry
	Failures    FailureReporter
}

type Service struct {
	dependencies Dependencies
	definitions  *definitionCatalog
	mutex        sync.Mutex
	workers      map[session.SessionID]*boundWorker
	parents      map[session.SessionID]struct{}
	closing      bool
}

func New(ctx context.Context, dependencies Dependencies) (*Service, error) {
	if ctx == nil {
		return nil, errors.New("subagent: Bound New Context is nil")
	}
	if dependencies.Agents == nil || dependencies.History == nil || dependencies.Definitions == nil || dependencies.Extensions == nil || dependencies.Executions == nil {
		return nil, errors.New("subagent: Bound requires Agent, history, Definition, Extension, and Execution capabilities")
	}
	owner := &Service{dependencies: dependencies, workers: make(map[session.SessionID]*boundWorker), parents: make(map[session.SessionID]struct{})}
	definitions, err := newDefinitionCatalog(ctx, dependencies.Definitions, owner)
	if err != nil {
		return nil, err
	}
	owner.definitions = definitions
	return owner, nil
}
func (*Service) Mode() subagent.Mode { return subagent.ModeBound }
func (owner *Service) List(ctx context.Context) ([]boundcontract.Definition, error) {
	return owner.definitions.List(ctx)
}
func (owner *Service) Create(ctx context.Context, command boundcontract.Creation) (boundcontract.Definition, error) {
	return owner.definitions.Create(ctx, command)
}
func (owner *Service) Replace(ctx context.Context, command boundcontract.Replacement) (boundcontract.Definition, error) {
	return owner.definitions.Replace(ctx, command)
}

func (owner *Service) SessionStarted(parentID session.SessionID) {
	owner.mutex.Lock()
	if owner.closing {
		owner.mutex.Unlock()
		return
	}
	owner.parents[parentID] = struct{}{}
	owner.mutex.Unlock()
	go func() {
		if err := owner.reconcile(context.Background(), parentID); err != nil && owner.dependencies.Failures != nil {
			owner.dependencies.Failures.ReportBoundReconcileFailure(ReconcileFailure{ParentID: parentID, Error: err})
		}
	}()
}
func (owner *Service) DefinitionsChanged() {
	owner.mutex.Lock()
	parents := make([]session.SessionID, 0, len(owner.parents))
	for id := range owner.parents {
		parents = append(parents, id)
	}
	owner.mutex.Unlock()
	for _, id := range parents {
		owner.SessionStarted(id)
	}
}

func (owner *Service) reconcile(ctx context.Context, parentID session.SessionID) error {
	parent, view, err := owner.parent(ctx, parentID)
	if err != nil {
		return err
	}
	projection, err := subagentprojection.FoldBound(view.Snapshot.Events)
	if err != nil {
		return err
	}
	var pending []pendingBinding
	for _, definition := range owner.definitions.enabled() {
		if _, found := projection.BindingNamed(definition.Name); found {
			continue
		}
		childID, idErr := sharedexecution.NewChildID()
		if idErr != nil {
			return idErr
		}
		pending = append(pending, pendingBinding{name: definition.Name, childSessionID: childID})
	}
	if len(pending) != 0 {
		result, commitErr := parent.Commit(ctx, bindingRegistration{bindings: pending})
		if commitErr != nil {
			return commitErr
		}
		if err = parent.Flush(ctx, result.Barrier); err != nil {
			return err
		}
		updated, readErr := parent.Read(ctx)
		err = readErr
		if err != nil {
			return err
		}
		view = sessionactorView{Header: updated.Header, Snapshot: updated.Snapshot}
		projection, err = subagentprojection.FoldBound(view.Snapshot.Events)
		if err != nil {
			return err
		}
	}
	for _, binding := range projection.Bindings {
		definition, found := owner.definitions.find(binding.Name)
		if !found || !definition.Enabled {
			continue
		}
		worker := owner.acquire(parentID, binding)
		if err = worker.materialize(ctx, parent, view, definition); err != nil {
			return err
		}
	}
	return nil
}

func (owner *Service) Deliver(ctx context.Context, address boundcontract.Address, input boundcontract.Input) (boundcontract.Receipt, error) {
	if err := checkContext(ctx, "Bound Deliver"); err != nil {
		return boundcontract.Receipt{}, err
	}
	if address.SessionID == "" || address.Name == "" {
		return boundcontract.Receipt{}, errors.New("subagent: Bound address is incomplete")
	}
	detached, err := boundcontract.SnapshotInput(input)
	if err != nil {
		return boundcontract.Receipt{}, err
	}
	parent, view, err := owner.parent(ctx, address.SessionID)
	if err != nil {
		return boundcontract.Receipt{}, err
	}
	projection, err := subagentprojection.FoldBound(view.Snapshot.Events)
	if err != nil {
		return boundcontract.Receipt{}, err
	}
	binding, found := projection.BindingNamed(address.Name)
	if !found {
		return boundcontract.Receipt{}, namedBindingNotFound(address.Name)
	}
	definition, found := owner.definitions.find(binding.Name)
	if !found || !definition.Enabled {
		return boundcontract.Receipt{}, boundDisabled(binding.ChildSessionID)
	}
	worker := owner.acquire(address.SessionID, binding)
	if err = worker.materialize(ctx, parent, view, definition); err != nil {
		return boundcontract.Receipt{}, err
	}
	return worker.deliver(ctx, detached)
}
func (owner *Service) HasBinding(ctx context.Context, parentID, childID session.SessionID) (bool, error) {
	_, view, err := owner.parent(ctx, parentID)
	if err != nil {
		return false, err
	}
	projection, err := subagentprojection.FoldBound(view.Snapshot.Events)
	if err != nil {
		return false, err
	}
	_, found := projection.Binding(childID)
	return found, nil
}
func (owner *Service) Followup(ctx context.Context, parentID, childID session.SessionID, message agentmessage.UserMessage) (agentmessage.MessageID, error) {
	parent, view, err := owner.parent(ctx, parentID)
	if err != nil {
		return "", err
	}
	projection, err := subagentprojection.FoldBound(view.Snapshot.Events)
	if err != nil {
		return "", err
	}
	binding, found := projection.Binding(childID)
	if !found {
		return "", bindingNotFound(childID)
	}
	definition, found := owner.definitions.find(binding.Name)
	if !found || !definition.Enabled {
		return "", boundDisabled(childID)
	}
	worker := owner.acquire(parentID, binding)
	if err = worker.materialize(ctx, parent, view, definition); err != nil {
		return "", err
	}
	return worker.followup(ctx, message)
}
func (owner *Service) Interrupt(ctx context.Context, childID session.SessionID) error {
	owner.mutex.Lock()
	worker := owner.workers[childID]
	owner.mutex.Unlock()
	if worker == nil {
		return nil
	}
	return worker.interrupt(ctx)
}
func (owner *Service) Close(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	owner.mutex.Lock()
	owner.closing = true
	workers := make([]*boundWorker, 0, len(owner.workers))
	for _, worker := range owner.workers {
		workers = append(workers, worker)
	}
	owner.mutex.Unlock()
	var closeErr error
	for _, worker := range workers {
		closeErr = errors.Join(closeErr, worker.close(ctx))
	}
	return errors.Join(closeErr, owner.definitions.Close(ctx))
}

func (owner *Service) parent(ctx context.Context, id session.SessionID) (agentclient.Agent, sessionactorView, error) {
	parent, err := owner.dependencies.Agents.Find(ctx, id)
	if err != nil {
		return nil, sessionactorView{}, &subagent.Error{Code: subagent.ErrorUnauthorized, Message: fmt.Sprintf("Bound operation requires exact live parent Agent %q", id), Cause: err}
	}
	view, err := parent.Read(ctx)
	if err != nil {
		return nil, sessionactorView{}, err
	}
	if view.Header.Origin != "" || view.Header.ParentSession != nil {
		return nil, sessionactorView{}, &subagent.Error{Code: subagent.ErrorUnauthorized, Message: fmt.Sprintf("Bound operation requires direct user Session %q", id)}
	}
	return parent, sessionactorView{Header: view.Header, Snapshot: view.Snapshot}, nil
}

type sessionactorView struct {
	Header   session.Header
	Snapshot session.Snapshot
}

func (owner *Service) acquire(parentID session.SessionID, binding subagentprojection.BoundBinding) *boundWorker {
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	current := owner.workers[binding.ChildSessionID]
	if current != nil && current.parentID == parentID && current.binding.Name == binding.Name {
		return current
	}
	worker := &boundWorker{owner: owner, parentID: parentID, binding: binding}
	owner.workers[binding.ChildSessionID] = worker
	return worker
}
func checkContext(ctx context.Context, name string) error {
	if ctx == nil {
		return errors.New("subagent: " + name + " Context is nil")
	}
	return context.Cause(ctx)
}

var _ boundcontract.Definitions = (*Service)(nil)
var _ boundcontract.Inbox = (*Service)(nil)
var _ DefinitionReconciler = (*Service)(nil)
