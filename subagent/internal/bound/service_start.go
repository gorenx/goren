package bound

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
	sharedexecution "github.com/gorenx/goren/subagent/internal/execution"
)

// Start materializes the child selected by one already-committed binding.
func (owner *Service) Start(
	ctx context.Context,
	command subagent.BoundStartCommand,
) (subagent.Execution, error) {
	if err := checkContext(ctx, "Bound Start"); err != nil {
		return nil, err
	}
	if err := owner.authorizeParent(command.Parent()); err != nil {
		return nil, err
	}
	currentOperation := owner.childOperation(
		command.Parent().ID(),
		command.ChildID(),
	)
	currentOperation.mutex.Lock()
	defer currentOperation.mutex.Unlock()
	return owner.startLocked(
		ctx,
		command.Parent(),
		command.ChildID(),
		currentOperation,
	)
}

// StartBindings attempts every committed binding without returning a child
// failure to the Agent Session-start publication path.
func (owner *Service) StartBindings(
	ctx context.Context,
	parentAgent agent.Agent,
) error {
	if err := checkContext(ctx, "Bound StartBindings"); err != nil {
		return err
	}
	if parentAgent == nil || parentAgent.SessionValue() == nil {
		return nil
	}
	view, err := owner.parentView(parentAgent.SessionValue())
	if err != nil {
		return err
	}
	if len(view.Bindings) == 0 {
		return nil
	}
	if owner.dependencies.Agents == nil ||
		!owner.dependencies.Agents.Contains(parentAgent) {
		return nil
	}
	for _, binding := range view.Bindings {
		command, commandErr := subagent.NewBoundStart(
			parentAgent,
			binding.ChildSessionID,
		)
		if commandErr != nil {
			owner.reportMaterializationFailure(
				parentAgent.ID(),
				binding.ChildSessionID,
				commandErr,
			)
			continue
		}
		_, _ = owner.Start(ctx, command)
	}
	return nil
}

func (owner *Service) startLocked(
	ctx context.Context,
	parentAgent agent.Agent,
	childID session.SessionID,
	currentOperation *operation,
) (subagent.Execution, error) {
	if err := checkContext(ctx, "Bound Start"); err != nil {
		return nil, err
	}
	if err := owner.authorizeParent(parentAgent); err != nil {
		return nil, err
	}
	if err := owner.requireMaterializationDependencies(); err != nil {
		return nil, err
	}
	view, err := owner.parentView(parentAgent.SessionValue())
	if err != nil {
		return nil, err
	}
	binding, found := view.Binding(childID)
	if !found {
		return nil, bindingNotFound(childID)
	}
	config, found := view.Config(childID)
	if !found {
		return nil, errors.New("subagent: Bound binding has no config")
	}
	current := currentOperation.loadCurrent()
	if current != nil {
		if current.running.State() != subagent.ExecutionActive {
			return nil, errors.New("subagent: Bound child is stopping")
		}
		if current.revision == config.Revision {
			if !config.Config.Enabled {
				return nil, boundDisabled(childID)
			}
			return current.running, nil
		}
		if err = current.terminator.handle.Subject.WhenIdle(ctx); err != nil {
			return nil, err
		}
		if err = current.running.StopAndWait(
			ctx,
			sharedexecution.StopNormal,
		); err != nil {
			return nil, err
		}
	}
	if !config.Config.Enabled {
		return nil, boundDisabled(childID)
	}
	handle, initialPromptNeeded, materializationErr := owner.materialize(
		ctx,
		parentAgent,
		binding,
		config,
	)
	if materializationErr != nil {
		return nil, owner.finishFailedMaterialization(
			ctx,
			parentAgent,
			childID,
			config.Revision,
			materializationErr,
		)
	}
	if initialPromptNeeded {
		messageValue, messageErr := agentmessage.NewUserMessage(
			agentmessage.UserMessageInput{
				Content: binding.Creation.InitialPrompt,
				Source:  agentmessage.UserMessageSource{},
			},
		)
		if messageErr != nil {
			return nil, owner.disposeFailedMaterialization(
				ctx,
				parentAgent,
				childID,
				config.Revision,
				handle,
				messageErr,
			)
		}
		if messageErr = handle.Subject.Followup(messageValue); messageErr != nil {
			return nil, owner.disposeFailedMaterialization(
				ctx,
				parentAgent,
				childID,
				config.Revision,
				handle,
				messageErr,
			)
		}
	}
	if err = owner.dependencies.Sessions.Flush(
		ctx,
		handle.Subject.SessionValue(),
	); err != nil {
		return nil, owner.disposeFailedMaterialization(
			ctx,
			parentAgent,
			childID,
			config.Revision,
			handle,
			err,
		)
	}
	current, err = owner.publish(
		handle,
		parentAgent,
		binding.Creation.SeedBuilder,
		config.Revision,
		currentOperation,
	)
	if err != nil {
		return nil, owner.disposeFailedMaterialization(
			ctx,
			parentAgent,
			childID,
			config.Revision,
			handle,
			err,
		)
	}
	if err = owner.recordMaterialization(
		ctx,
		parentAgent,
		childID,
		config.Revision,
		subagent.BoundMaterializationSucceeded,
	); err != nil {
		stopErr := current.running.StopAndWait(
			context.WithoutCancel(ctx),
			sharedexecution.StopModule,
		)
		return nil, errors.Join(
			owner.finishFailedMaterialization(
				ctx,
				parentAgent,
				childID,
				config.Revision,
				err,
			),
			stopErr,
		)
	}
	owner.ensureInteractionWorker(
		parentAgent,
		binding,
		handle.Subject.SessionValue(),
		currentOperation,
	)
	owner.watch(current)
	return current.running, nil
}
