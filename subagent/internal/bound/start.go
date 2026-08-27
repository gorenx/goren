package bound

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	sesspersist "github.com/gorenx/goren/session/persistence"
	"github.com/gorenx/goren/subagent"
	sharedexecution "github.com/gorenx/goren/subagent/internal/execution"
	"github.com/gorenx/goren/subagent/internal/lineage"
	subagentprojection "github.com/gorenx/goren/subagent/internal/projection"
	"github.com/gorenx/goren/subagent/internal/seedbuilder"
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
				Source: agentmessage.UserMessageSource{
					Kind: "user",
				},
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

func (owner *Service) materialize(
	ctx context.Context,
	parentAgent agent.Agent,
	binding subagentprojection.BoundBinding,
	config subagentprojection.BoundConfig,
) (agent.Handle, bool, error) {
	inspection, err := owner.dependencies.Persistence.Inspect(
		ctx,
		binding.ChildSessionID,
	)
	if err == nil {
		handle, resumeErr := owner.resume(
			ctx,
			parentAgent,
			binding,
			config,
			inspection,
		)
		return handle, !hasSubmittedMessage(inspection), resumeErr
	}
	var notFound *sesspersist.NotFoundError
	if !errors.As(err, &notFound) {
		return agent.Handle{}, false, err
	}
	handle, createErr := owner.create(
		ctx,
		parentAgent,
		binding,
		config,
	)
	return handle, true, createErr
}

func (owner *Service) create(
	ctx context.Context,
	parentAgent agent.Agent,
	binding subagentprojection.BoundBinding,
	config subagentprojection.BoundConfig,
) (agent.Handle, error) {
	builder, found := owner.dependencies.SeedBuilders.Find(
		binding.Creation.SeedBuilder,
	)
	if !found {
		return agent.Handle{}, noSeedBuilder(binding.Creation.SeedBuilder)
	}
	seedValue, err := builder.BuildSeed(
		ctx,
		parentAgent.SessionValue().Events(),
	)
	if err != nil {
		return agent.Handle{}, err
	}
	builderSeed := seedValue.EventPrefix()
	descriptor := subagent.BoundDescriptor{
		Provider: binding.Creation.SeedBuilder,
		Label:    binding.Creation.Title,
	}
	seed, err := seedbuilder.AppendDescriptor(
		binding.ChildSessionID,
		builderSeed,
		descriptor,
	)
	if err != nil {
		return agent.Handle{}, err
	}
	childLineage, err := lineage.From(parentAgent, nil)
	if err != nil {
		return agent.Handle{}, err
	}
	configProvisioner, err := owner.provisioner(
		config.Config,
		true,
	)
	if err != nil {
		return agent.Handle{}, err
	}
	initiated, err := agent.WithInitiator(ctx, parentAgent)
	if err != nil {
		return agent.Handle{}, err
	}
	handle, err := owner.dependencies.Constructor.Create(
		initiated,
		agent.CreateOptions{
			SessionID:    binding.ChildSessionID,
			Metadata:     childLineage.Metadata(int64(len(builderSeed))),
			Seed:         seed,
			AgentOptions: binding.Creation.AgentOptions,
			Provisioner: agent.ComposeProvisioners(
				configProvisioner,
				newAppliedProvisioner(parentAgent.ID(), config),
			),
			RuntimeParent: parentAgent,
		},
	)
	if errors.Is(err, agent.ErrDescendantAdmissionClosed) {
		return agent.Handle{}, descendantAdmissionClosed(binding.ChildSessionID)
	}
	return handle, err
}

func (owner *Service) resume(
	ctx context.Context,
	parentAgent agent.Agent,
	binding subagentprojection.BoundBinding,
	config subagentprojection.BoundConfig,
	inspection sesspersist.Inspection,
) (agent.Handle, error) {
	if inspection.Header.ParentSession == nil ||
		*inspection.Header.ParentSession != parentAgent.ID() {
		return agent.Handle{}, unauthorizedChild(binding.ChildSessionID)
	}
	suffixStart := int64(0)
	if inspection.Header.SeedLength != nil {
		suffixStart = *inspection.Header.SeedLength
	}
	if suffixStart < 0 || suffixStart > int64(len(inspection.Events)) {
		return agent.Handle{}, notResumable(
			binding.ChildSessionID,
			"has an invalid seed boundary",
			nil,
		)
	}
	descriptorValue, found, err := subagent.FoldDescriptor(
		inspection.Events[suffixStart:],
	)
	if err != nil || !found {
		return agent.Handle{}, notResumable(
			binding.ChildSessionID,
			"has no Bound descriptor",
			err,
		)
	}
	descriptor, matches := descriptorValue.(subagent.BoundDescriptor)
	if !matches || descriptor.Provider != binding.Creation.SeedBuilder ||
		descriptor.Label != binding.Creation.Title {
		return agent.Handle{}, notResumable(
			binding.ChildSessionID,
			"does not match its Bound binding",
			nil,
		)
	}
	configProvisioner, err := owner.provisioner(
		config.Config,
		false,
	)
	if err != nil {
		return agent.Handle{}, err
	}
	initiated, err := agent.WithInitiator(ctx, parentAgent)
	if err != nil {
		return agent.Handle{}, err
	}
	handle, err := owner.dependencies.Constructor.Resume(
		initiated,
		agent.ResumeOptions{
			SessionID:    binding.ChildSessionID,
			AgentOptions: binding.Creation.AgentOptions,
			Provisioner: agent.ComposeProvisioners(
				configProvisioner,
				newAppliedProvisioner(parentAgent.ID(), config),
			),
			RuntimeParent: parentAgent,
		},
	)
	if errors.Is(err, agent.ErrDescendantAdmissionClosed) {
		return agent.Handle{}, descendantAdmissionClosed(binding.ChildSessionID)
	}
	if err != nil {
		return agent.Handle{}, notResumable(
			binding.ChildSessionID,
			"is unavailable",
			err,
		)
	}
	return handle, nil
}

func newAppliedProvisioner(
	parentID session.SessionID,
	config subagentprojection.BoundConfig,
) agent.Provisioner {
	return &appliedProvisioner{
		parentID:       parentID,
		configEventSeq: config.Seq,
		revision:       config.Revision,
	}
}

func hasSubmittedMessage(inspection sesspersist.Inspection) bool {
	childStart := int64(0)
	if inspection.Header.SeedLength != nil {
		childStart = *inspection.Header.SeedLength
	}
	for _, committed := range inspection.Events {
		if committed.Seq < childStart {
			continue
		}
		if committed.Type != agent.InboxSplicedEventName {
			continue
		}
		var splice agent.InboxSplice
		if err := json.Unmarshal(committed.Data, &splice); err != nil {
			continue
		}
		if len(splice.Inserted) != 0 {
			return true
		}
	}
	return false
}

func (owner *Service) requireMaterializationDependencies() error {
	if owner.dependencies.Constructor == nil ||
		owner.dependencies.Sessions == nil ||
		owner.dependencies.Persistence == nil ||
		owner.dependencies.SeedBuilders == nil ||
		owner.dependencies.Executions == nil {
		return unavailableDependency("materialization dependencies")
	}
	return nil
}

func (owner *Service) finishFailedMaterialization(
	ctx context.Context,
	parentAgent agent.Agent,
	childID session.SessionID,
	revision int64,
	materializationErr error,
) error {
	owner.reportMaterializationFailure(
		parentAgent.ID(),
		childID,
		materializationErr,
	)
	stateErr := owner.recordMaterialization(
		context.WithoutCancel(ctx),
		parentAgent,
		childID,
		revision,
		subagent.BoundMaterializationFailed,
	)
	return errors.Join(materializationErr, stateErr)
}

func (owner *Service) disposeFailedMaterialization(
	ctx context.Context,
	parentAgent agent.Agent,
	childID session.SessionID,
	revision int64,
	handle agent.Handle,
	materializationErr error,
) error {
	disposeErr := handle.Dispose(context.WithoutCancel(ctx))
	return errors.Join(
		owner.finishFailedMaterialization(
			ctx,
			parentAgent,
			childID,
			revision,
			materializationErr,
		),
		disposeErr,
	)
}

func (owner *Service) recordMaterialization(
	ctx context.Context,
	parentAgent agent.Agent,
	childID session.SessionID,
	revision int64,
	result subagent.BoundMaterializationResult,
) error {
	draft, err := session.NewEventDraft(
		subagent.BoundMaterializationEvent,
		subagent.BoundMaterializationData{
			Version:        subagent.BoundEventVersion,
			ChildSessionID: childID,
			ConfigRevision: revision,
			Result:         result,
		},
	)
	if err != nil {
		return err
	}
	if _, err = parentAgent.SessionValue().Commit(
		ctx,
		session.Batch(draft),
	); err != nil {
		return err
	}
	return owner.dependencies.Sessions.Flush(
		ctx,
		parentAgent.SessionValue(),
	)
}

func (owner *Service) reportMaterializationFailure(
	parentID session.SessionID,
	childID session.SessionID,
	materializationErr error,
) {
	if owner.dependencies.Failures == nil {
		return
	}
	owner.dependencies.Failures.ReportBoundMaterializationFailure(
		MaterializationFailure{
			ParentID: parentID,
			ChildID:  childID,
			Error:    materializationErr,
		},
	)
}

func boundDisabled(childID session.SessionID) error {
	return &subagent.Error{
		Code: subagent.ErrorBoundDisabled,
		Message: fmt.Sprintf(
			"subagent %q is disabled by its latest Bound config",
			childID,
		),
	}
}

func unauthorizedChild(childID session.SessionID) error {
	return &subagent.Error{
		Code: subagent.ErrorUnauthorized,
		Message: fmt.Sprintf(
			"subagent %q belongs to another parent Session",
			childID,
		),
	}
}

func notResumable(
	childID session.SessionID,
	reason string,
	cause error,
) error {
	return &subagent.Error{
		Code: subagent.ErrorNotResumable,
		Message: fmt.Sprintf(
			"subagent %q %s",
			childID,
			reason,
		),
		Cause: cause,
	}
}

func descendantAdmissionClosed(childID session.SessionID) error {
	return &subagent.Error{
		Code: subagent.ErrorDraining,
		Message: fmt.Sprintf(
			"subagent %q lost parent descendant admission",
			childID,
		),
	}
}
