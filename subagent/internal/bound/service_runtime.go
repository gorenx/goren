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
	slot := owner.bindings.child(
		command.Parent().ID(),
		command.ChildID(),
	)
	slot.mutex.Lock()
	defer slot.mutex.Unlock()
	return owner.startLocked(
		ctx,
		command.Parent(),
		command.ChildID(),
		slot,
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
	view, err := readBoundProjection(
		owner.dependencies.Projections,
		parentAgent.SessionValue(),
	)
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
	slot *bindingSlot,
) (subagent.Execution, error) {
	if err := checkContext(ctx, "Bound Start"); err != nil {
		return nil, err
	}
	if err := owner.authorizeParent(parentAgent); err != nil {
		return nil, err
	}
	if err := owner.requireRuntimeDependencies(); err != nil {
		return nil, err
	}
	view, err := readBoundProjection(
		owner.dependencies.Projections,
		parentAgent.SessionValue(),
	)
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
	current := slot.loadCurrent()
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
	handle, initialPromptNeeded, materializationErr := owner.materializer.materialize(
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
	current, err = owner.residents.publish(
		handle,
		parentAgent,
		binding.Creation.SeedBuilder,
		config.Revision,
		slot,
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
	owner.interactions.ensure(
		parentAgent,
		binding,
		handle.Subject.SessionValue(),
		slot,
	)
	owner.residents.watch(current)
	return current.running, nil
}

func (owner *Service) requireRuntimeDependencies() error {
	if err := owner.materializer.requireDependencies(); err != nil {
		return err
	}
	if owner.dependencies.Sessions == nil || owner.dependencies.Executions == nil {
		return unavailableDependency("runtime dependencies")
	}
	return nil
}

// Send ensures the latest committed Bound config has one resident Agent epoch
// and then admits the message to that exact epoch under the same operation
// lock used by config replacement.
func (owner *Service) Send(
	ctx context.Context,
	parentAgent agent.Agent,
	childID session.SessionID,
	messageValue agentmessage.UserMessage,
) (agentmessage.MessageID, error) {
	if err := checkContext(ctx, "Bound Send"); err != nil {
		return "", err
	}
	if err := owner.authorizeParent(parentAgent); err != nil {
		return "", err
	}
	slot := owner.bindings.child(parentAgent.ID(), childID)
	slot.mutex.Lock()
	defer slot.mutex.Unlock()
	if _, err := owner.startLocked(
		ctx,
		parentAgent,
		childID,
		slot,
	); err != nil {
		return "", err
	}
	current := slot.loadCurrent()
	if current == nil || !agent.Same(
		current.terminator.parent,
		parentAgent,
	) {
		return "", errors.New(
			"subagent: Bound resident Agent is unavailable",
		)
	}
	if err := current.terminator.handle.Subject.Followup(
		messageValue,
	); err != nil {
		return "", err
	}
	return messageValue.StableID(), nil
}

// Interrupt cancels the current turn but retains queued Bound work and the
// resident Agent epoch.
func (owner *Service) Interrupt(
	ctx context.Context,
	childID session.SessionID,
) error {
	if err := checkContext(ctx, "Bound Interrupt"); err != nil {
		return err
	}
	owner.residents.interrupt(childID)
	return nil
}

// Close stops every Bound epoch owned by this Service.
func (owner *Service) Close(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	owner.interactions.close()
	return owner.residents.close(ctx)
}

func (owner *Service) replaceResidentLocked(
	ctx context.Context,
	parentAgent agent.Agent,
	childID session.SessionID,
) error {
	slot := owner.bindings.child(parentAgent.ID(), childID)
	current := slot.loadCurrent()
	if current == nil {
		return nil
	}
	if !agent.Same(current.terminator.parent, parentAgent) {
		return unauthorizedChild(childID)
	}
	_, err := owner.startLocked(
		ctx,
		parentAgent,
		childID,
		slot,
	)
	var typed *subagent.Error
	if errors.As(err, &typed) && typed.Code == subagent.ErrorBoundDisabled {
		return nil
	}
	return err
}
