package bound

import (
	"context"
	"errors"
	"fmt"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/subagent"
	boundcontract "github.com/gorenx/goren/subagent/bound"
	sharedexecution "github.com/gorenx/goren/subagent/internal/execution"
)

func (child *boundChild) receivingEpoch(
	requestContext context.Context,
) (*residentEpoch, error) {
	current, err := child.reconcileExecution(requestContext)
	if err != nil {
		return nil, err
	}
	if current == nil {
		return nil, boundDisabled(child.key.childSessionID)
	}
	return current, nil
}

func (child *boundChild) reconcileExecution(
	requestContext context.Context,
) (*residentEpoch, error) {
	workContext, cancelWork := child.operationContext(requestContext)
	defer cancelWork()
	if err := checkContext(workContext, "Bound reconciliation"); err != nil {
		return nil, err
	}
	if err := child.authorizeParent(); err != nil {
		return nil, err
	}
	if err := child.requireDependencies(); err != nil {
		return nil, err
	}
	view, err := readBoundProjection(
		child.projections,
		child.parent.SessionValue(),
	)
	if err != nil {
		return nil, err
	}
	bindingValue, found := view.BindingNamed(child.key.name)
	if !found || bindingValue.ChildSessionID != child.key.childSessionID {
		return nil, bindingNotFound(child.key.childSessionID)
	}
	definitionValue, found := child.definitions.find(child.key.name)
	if !found {
		return nil, fmt.Errorf(
			"subagent: Bound Binding %q has no Definition",
			child.key.name,
		)
	}
	current := child.current
	if current != nil {
		phase := current.State()
		if phase == subagent.ExecutionStopped {
			child.current = nil
			current = nil
		} else if phase != subagent.ExecutionActive {
			return nil, errors.New("subagent: Bound child is stopping")
		}
	}
	if current != nil &&
		current.definitionRevision == definitionValue.Revision &&
		definitionValue.Enabled {
		return current, nil
	}
	if current != nil {
		if err = current.StopAndWait(
			workContext,
			sharedexecution.CloseNormal,
		); err != nil {
			return nil, err
		}
		if child.current == current {
			child.current = nil
		}
	}
	if !definitionValue.Enabled {
		return nil, nil
	}
	handle, materializationErr := child.materializer.materialize(
		workContext,
		child.parent,
		bindingValue,
		definitionValue,
	)
	if materializationErr != nil {
		return nil, child.finishFailedMaterialization(
			workContext,
			definitionValue.Revision,
			materializationErr,
		)
	}
	if err = child.sessions.Flush(
		workContext,
		handle.Subject.SessionValue(),
	); err != nil {
		return nil, child.disposeFailedMaterialization(
			workContext,
			definitionValue.Revision,
			handle,
			err,
		)
	}
	current, err = child.publish(handle, definitionValue.Revision)
	if err != nil {
		return nil, child.disposeFailedMaterialization(
			workContext,
			definitionValue.Revision,
			handle,
			err,
		)
	}
	if err = child.recordMaterialization(
		workContext,
		definitionValue.Revision,
		boundcontract.MaterializationSucceeded,
	); err != nil {
		stopErr := current.StopAndWait(
			context.WithoutCancel(workContext),
			sharedexecution.CloseModule,
		)
		if child.current == current {
			child.current = nil
		}
		return nil, errors.Join(
			child.finishFailedMaterialization(
				workContext,
				definitionValue.Revision,
				err,
			),
			stopErr,
		)
	}
	child.watch(current)
	return current, nil
}

func (child *boundChild) publish(
	handle agent.Handle,
	revision int64,
) (*residentEpoch, error) {
	executionRunID, err := sharedexecution.NewRunID()
	if err != nil {
		return nil, err
	}
	resident, err := newResidentEpoch(executionRunID, handle.Subject.ID())
	if err != nil {
		return nil, err
	}
	resident.handle = handle
	resident.definitionRevision = revision
	resident.provider = string(subagent.ModeBound)
	resident.sessions = child.sessions
	resident.failures = child.failures
	resident.publisher = child.publisher
	resident.parent = child.parent
	resident.executions = child.executions
	child.current = resident
	if err = sharedexecution.Publish(
		child.executions,
		child.publisher,
		sharedexecution.Entry{
			Execution: resident,
			Mode:      subagent.ModeBound,
			Parent:    child.parent,
			Subject:   handle.Subject,
			Closing:   handle.ClosingSignal(),
		},
		resident.provider,
	); err != nil {
		if child.current == resident {
			child.current = nil
		}
		return nil, err
	}
	return resident, nil
}

// watch bridges the Agent-owned closing signal into exact Execution
// settlement. The signal is guaranteed to close with the epoch.
func (*boundChild) watch(current *residentEpoch) {
	go func() {
		<-current.handle.ClosingSignal()
		current.Stop(sharedexecution.CloseExternal)
	}()
}

func (child *boundChild) handleInterrupt() {
	current := child.current
	if current == nil || current.State() != subagent.ExecutionActive {
		return
	}
	current.handle.Subject.Cancel(
		agent.ParentCancel{},
		agent.CancelOptions{
			KeepInbox: true,
		},
	)
}

func (child *boundChild) stopCurrent(requestContext context.Context) error {
	current := child.current
	if current == nil {
		return nil
	}
	err := current.StopAndWait(
		requestContext,
		sharedexecution.CloseModule,
	)
	if child.current == current {
		child.current = nil
	}
	return err
}

func (child *boundChild) authorizeParent() error {
	if child.parent == nil || child.agents == nil ||
		!child.agents.Contains(child.parent) || !isUserAgent(child.parent) {
		return &subagent.Error{
			Code: subagent.ErrorUnauthorized,
			Message: fmt.Sprintf(
				"Bound operation requires exact live user Agent %q",
				child.key.parentID,
			),
		}
	}
	return nil
}

func (child *boundChild) requireDependencies() error {
	if err := child.materializer.requireDependencies(); err != nil {
		return err
	}
	if child.sessions == nil || child.executions == nil ||
		child.definitions == nil {
		return unavailableDependency("child dependencies")
	}
	return nil
}
