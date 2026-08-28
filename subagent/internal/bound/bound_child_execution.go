package bound

import (
	"context"
	"errors"
	"fmt"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/subagent"
	sharedexecution "github.com/gorenx/goren/subagent/internal/execution"
)

func (child *boundChild) handleStart(
	requestContext context.Context,
) (subagent.Execution, error) {
	workContext, cancelWork := child.operationContext(requestContext)
	defer cancelWork()
	if err := checkContext(workContext, "Bound Start"); err != nil {
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
	binding, found := view.Binding(child.key.childID)
	if !found {
		return nil, bindingNotFound(child.key.childID)
	}
	config, found := view.Config(child.key.childID)
	if !found {
		return nil, errors.New("subagent: Bound binding has no config")
	}
	current := child.current
	if current != nil {
		state := current.execution.State()
		if state == subagent.ExecutionStopped {
			child.current = nil
			current = nil
		} else if state != subagent.ExecutionActive {
			return nil, errors.New("subagent: Bound child is stopping")
		}
	}
	if current != nil {
		if current.configRevision == config.Revision {
			if !config.Config.Enabled {
				return nil, boundDisabled(child.key.childID)
			}
			return current.execution, nil
		}
		if err = current.execution.StopAndWait(
			workContext,
			sharedexecution.StopNormal,
		); err != nil {
			return nil, err
		}
		if child.current == current {
			child.current = nil
		}
	}
	if !config.Config.Enabled {
		return nil, boundDisabled(child.key.childID)
	}
	epoch, materializationErr := child.materializer.materialize(
		workContext,
		child.parent,
		binding,
		config,
	)
	if materializationErr != nil {
		return nil, child.finishFailedMaterialization(
			workContext,
			config.Revision,
			materializationErr,
		)
	}
	handle := epoch.handle
	if epoch.initialPromptPending {
		messageValue, messageErr := agentmessage.NewUserMessage(
			agentmessage.UserMessageInput{
				Content: binding.Creation.InitialPrompt,
				Source:  agentmessage.UserMessageSource{},
			},
		)
		if messageErr != nil {
			return nil, child.disposeFailedMaterialization(
				workContext,
				config.Revision,
				handle,
				messageErr,
			)
		}
		if messageErr = handle.Subject.Followup(messageValue); messageErr != nil {
			return nil, child.disposeFailedMaterialization(
				workContext,
				config.Revision,
				handle,
				messageErr,
			)
		}
	}
	if err = child.sessions.Flush(
		workContext,
		handle.Subject.SessionValue(),
	); err != nil {
		return nil, child.disposeFailedMaterialization(
			workContext,
			config.Revision,
			handle,
			err,
		)
	}
	current, err = child.publish(
		handle,
		binding.Creation.SeedBuilder,
		config.Revision,
	)
	if err != nil {
		return nil, child.disposeFailedMaterialization(
			workContext,
			config.Revision,
			handle,
			err,
		)
	}
	if err = child.recordMaterialization(
		workContext,
		config.Revision,
		subagent.BoundMaterializationSucceeded,
	); err != nil {
		stopErr := current.execution.StopAndWait(
			context.WithoutCancel(workContext),
			sharedexecution.StopModule,
		)
		if child.current == current {
			child.current = nil
		}
		return nil, errors.Join(
			child.finishFailedMaterialization(
				workContext,
				config.Revision,
				err,
			),
			stopErr,
		)
	}
	child.initializeDelivery(binding, handle.Subject.SessionValue())
	child.watch(current)
	child.notify()
	return current.execution, nil
}

func (child *boundChild) publish(
	handle agent.Handle,
	seedBuilder string,
	revision int64,
) (*residentEpoch, error) {
	runID, err := sharedexecution.NewRunID()
	if err != nil {
		return nil, err
	}
	resident := &residentEpoch{
		owner:          child,
		handle:         handle,
		configRevision: revision,
		seedBuilder:    seedBuilder,
	}
	running, err := sharedexecution.New(
		runID,
		handle.Subject.ID(),
		resident,
	)
	if err != nil {
		return nil, err
	}
	resident.execution = running
	child.current = resident
	if err = sharedexecution.Publish(
		child.executions,
		child.publisher,
		sharedexecution.Entry{
			Execution: running,
			Mode:      subagent.ModeBound,
			Parent:    child.parent,
			Subject:   handle.Subject,
			Closing:   handle.ClosingSignal(),
		},
		seedBuilder,
	); err != nil {
		if child.current == resident {
			child.current = nil
		}
		return nil, err
	}
	return resident, nil
}

// watch bridges the Agent-owned closing signal into Execution settlement. The
// Agent lifecycle closes the signal for explicit child disposal and structural
// parent disposal, so this observer has the same lifetime as the exact child
// epoch and must outlive the boundChild when parent teardown has begun.
func (*boundChild) watch(current *residentEpoch) {
	go func() {
		<-current.handle.ClosingSignal()
		current.execution.Stop(sharedexecution.StopExternal)
	}()
}

func (child *boundChild) handleInterrupt() {
	current := child.current
	if current == nil || current.execution.State() != subagent.ExecutionActive {
		return
	}
	current.handle.Subject.Cancel(
		agent.ParentCancel{},
		agent.CancelOptions{
			KeepInbox: true,
		},
	)
}

func (child *boundChild) stopCurrent(ctx context.Context) error {
	current := child.current
	if current == nil {
		return nil
	}
	err := current.execution.StopAndWait(ctx, sharedexecution.StopModule)
	if child.current == current {
		child.current = nil
	}
	return err
}

func (child *boundChild) authorizeParent() error {
	if child.parent == nil || child.agents == nil ||
		!child.agents.Contains(child.parent) {
		return &subagent.Error{
			Code: subagent.ErrorUnauthorized,
			Message: fmt.Sprintf(
				"Bound operation requires exact live parent Agent %q",
				child.key.parentID,
			),
		}
	}
	if child.parent.SessionValue() == nil {
		return errors.New("subagent: Bound parent Session is unavailable")
	}
	return nil
}

func (child *boundChild) requireDependencies() error {
	if err := child.materializer.requireDependencies(); err != nil {
		return err
	}
	if child.sessions == nil || child.executions == nil {
		return unavailableDependency("child dependencies")
	}
	return nil
}
