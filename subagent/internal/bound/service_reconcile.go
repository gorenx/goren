package bound

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
	sharedexecution "github.com/gorenx/goren/subagent/internal/execution"
	subagentprojection "github.com/gorenx/goren/subagent/internal/projection"
)

// reconcileParent relies on the parent Session FIFO for atomic Binding
// registration. It never reserves the parent Agent's maintenance activity.
func (owner *Service) reconcileParent(
	requestContext context.Context,
	parentAgent agent.Agent,
) error {
	if err := checkContext(requestContext, "Bound reconcile"); err != nil {
		return err
	}
	if owner.dependencies.Agents == nil ||
		!owner.dependencies.Agents.Contains(parentAgent) {
		return nil
	}
	bindings, err := owner.ensureBindings(
		requestContext,
		parentAgent,
	)
	if err != nil {
		return err
	}
	if owner.dependencies.Agents == nil ||
		!owner.dependencies.Agents.Contains(parentAgent) {
		return nil
	}
	return owner.activateBindings(
		requestContext,
		parentAgent,
		bindings,
	)
}

func (owner *Service) ensureBindings(
	requestContext context.Context,
	parentAgent agent.Agent,
) ([]subagentprojection.BoundBinding, error) {
	if err := owner.authorizeParent(parentAgent); err != nil {
		return nil, err
	}
	parentSession := parentAgent.SessionValue()
	view, err := readBoundProjection(
		owner.dependencies.Projections,
		parentSession,
	)
	if err != nil {
		return nil, err
	}
	definitions := owner.definitions.enabled()
	pending := make([]pendingBinding, 0, len(definitions))
	for _, definitionValue := range definitions {
		if _, found := view.BindingNamed(definitionValue.Name); found {
			continue
		}
		childSessionID, childErr := sharedexecution.NewChildID()
		if childErr != nil {
			return nil, childErr
		}
		pending = append(
			pending,
			pendingBinding{
				name:           definitionValue.Name,
				childSessionID: childSessionID,
			},
		)
	}
	if len(pending) == 0 {
		return append(
			[]subagentprojection.BoundBinding(nil),
			view.Bindings...,
		), nil
	}
	if _, err = parentSession.Commit(
		requestContext,
		bindingRegistration{
			bindings: pending,
		},
	); err != nil {
		return nil, err
	}
	if owner.dependencies.Sessions == nil {
		return nil, unavailableDependency("Session LiveStore")
	}
	if err = owner.dependencies.Sessions.Flush(
		requestContext,
		parentSession,
	); err != nil {
		return nil, err
	}
	view, err = readBoundProjection(
		owner.dependencies.Projections,
		parentSession,
	)
	if err != nil {
		return nil, err
	}
	return append([]subagentprojection.BoundBinding(nil), view.Bindings...), nil
}

func (owner *Service) activateBindings(
	requestContext context.Context,
	parentAgent agent.Agent,
	bindings []subagentprojection.BoundBinding,
) error {
	results := make(chan error, len(bindings))
	var activations sync.WaitGroup
	for _, bindingValue := range bindings {
		if _, found := owner.definitions.find(bindingValue.Name); !found {
			results <- fmt.Errorf(
				"subagent: Bound Binding %q has no Definition",
				bindingValue.Name,
			)
			continue
		}
		worker, err := owner.workers.acquire(parentAgent, bindingValue)
		if err != nil {
			results <- err
			continue
		}
		activations.Add(1)
		go func() {
			defer activations.Done()
			results <- worker.reconcile(requestContext)
		}()
	}
	activations.Wait()
	close(results)
	var activationErr error
	for err := range results {
		activationErr = errors.Join(activationErr, err)
	}
	return activationErr
}

func (owner *Service) reportReconcileFailure(
	parentID session.SessionID,
	reconcileErr error,
) {
	if owner.dependencies.Failures == nil || reconcileErr == nil {
		return
	}
	owner.dependencies.Failures.ReportBoundReconcileFailure(
		ReconcileFailure{
			ParentID: parentID,
			Error:    reconcileErr,
		},
	)
}
