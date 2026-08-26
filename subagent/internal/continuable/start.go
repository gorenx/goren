package continuable

import (
	"context"
	"errors"
	"fmt"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/subagent/internal/childrequest"
	sharedexecution "github.com/gorenx/goren/subagent/internal/execution"
	"github.com/gorenx/goren/subagent/internal/lineage"
)

// Start creates one durable child and returns its first common Execution.
func (owner *Service) Start(
	requestContext context.Context,
	command subagent.StartCommand,
) (subagent.Execution, error) {
	if contextErr := checkContext(requestContext, "Continuable Start"); contextErr != nil {
		return nil, contextErr
	}
	if command.Mode() != subagent.ModeContinuable {
		return nil, errors.New(
			"subagent: Continuable received another start mode",
		)
	}
	requestSnapshot, snapshotErr := childrequest.Snapshot(command.Request())
	if snapshotErr != nil {
		return nil, snapshotErr
	}
	if admissionErr := owner.assertAccepting(requestSnapshot.Parent); admissionErr != nil {
		return nil, admissionErr
	}
	childLineage, lineageErr := lineage.From(
		requestSnapshot.Parent,
		requestSnapshot.MaxDepth,
	)
	if lineageErr != nil {
		return nil, lineageErr
	}
	resolvedOptions := childLineage.AgentOptions(requestSnapshot.AgentOptions)
	requestSnapshot.AgentOptions = &resolvedOptions
	label := command.Label()
	if label == nil {
		return nil, errors.New("subagent: Continuable label is missing")
	}
	descriptor, descriptorErr := continuableDescriptor(
		command.SeedBuilderName(),
		*label,
		requestSnapshot,
	)
	if descriptorErr != nil {
		return nil, descriptorErr
	}
	childID, childIDErr := requestedChildID(command.RequestedChildID())
	if childIDErr != nil {
		return nil, childIDErr
	}
	builder, found := owner.dependencies.SeedBuilders.Find(
		command.SeedBuilderName(),
	)
	if !found {
		return nil, &subagent.Error{
			Code: subagent.ErrorNoSeedBuilder,
			Message: fmt.Sprintf(
				"no subagent SeedBuilder registered for %q",
				command.SeedBuilderName(),
			),
		}
	}
	parentSession := requestSnapshot.Parent.SessionValue()
	if parentSession == nil {
		return nil, errors.New("subagent: parent Session is unavailable")
	}
	parentHeader := parentSession.Header()
	seedValue, seedErr := builder.BuildSeed(
		requestContext,
		subagent.SeedRequest{
			ChildID: childID,
			Parent: subagent.ParentSnapshot{
				SessionID: parentHeader.ID,
				Header:    parentHeader,
				Events:    parentSession.Events(),
			},
		},
	)
	if seedErr != nil {
		return nil, seedErr
	}
	seed, seedErr := descriptorSeed(childID, seedValue.Events, descriptor)
	if seedErr != nil {
		return nil, seedErr
	}
	slot := owner.acquireSlot(childID)
	defer owner.releaseSlot(childID, slot)
	slot.mutex.Lock()
	defer slot.mutex.Unlock()
	if requestErr := requestContext.Err(); requestErr != nil {
		return nil, requestErr
	}
	if admissionErr := owner.assertAccepting(requestSnapshot.Parent); admissionErr != nil {
		return nil, admissionErr
	}
	if slot.current != nil {
		return nil, duplicateChild(childID)
	}
	if availabilityErr := owner.assertAvailable(
		requestContext,
		childID,
		command.RequestedChildID() != nil,
	); availabilityErr != nil {
		return nil, availabilityErr
	}
	handle, createErr := owner.create(
		requestContext,
		childID,
		descriptor,
		requestSnapshot,
		childLineage,
		seed,
		int64(len(seedValue.Events)),
	)
	if createErr != nil {
		return nil, createErr
	}
	prompt, messageErr := llm.NewUserMessage(llm.UserMessageInput{
		Content: requestSnapshot.Prompt,
		Source: llm.UserMessageSource{
			Kind: "user",
		},
	})
	if messageErr != nil {
		return nil, errors.Join(
			messageErr,
			handle.Dispose(context.WithoutCancel(requestContext)),
		)
	}
	if submitErr := handle.Subject.Followup(prompt); submitErr != nil {
		return nil, errors.Join(
			submitErr,
			handle.Dispose(context.WithoutCancel(requestContext)),
		)
	}
	current, publishErr := owner.publish(
		handle,
		requestSnapshot.Parent,
		command.SeedBuilderName(),
		slot,
	)
	if publishErr != nil {
		return nil, errors.Join(
			publishErr,
			handle.Dispose(context.WithoutCancel(requestContext)),
		)
	}
	slot.current = current
	owner.watch(current)
	return current.running, nil
}

func (owner *Service) create(
	requestContext context.Context,
	childID session.SessionID,
	descriptor subagent.ContinuableDescriptor,
	request subagent.ChildRequest,
	childLineage lineage.Lineage,
	seed []session.Event,
	lineageSeedLength int64,
) (agent.Handle, error) {
	initiatedContext, contextErr := agent.WithInitiator(
		requestContext,
		request.Parent,
	)
	if contextErr != nil {
		return agent.Handle{}, contextErr
	}
	handle, createErr := owner.dependencies.Constructor.Create(
		initiatedContext,
		agent.CreateOptions{
			SessionID:    childID,
			Metadata:     childLineage.Metadata(lineageSeedLength),
			Seed:         seed,
			AgentOptions: *request.AgentOptions,
			Provisioner: owner.provisioner(
				scopePolicy{
					childID:    childID,
					parentID:   request.Parent.ID(),
					descriptor: descriptor,
					fresh:      true,
				},
			),
			RuntimeParent: request.Parent,
		},
	)
	if createErr != nil {
		if errors.Is(createErr, agent.ErrDescendantAdmissionClosed) {
			return agent.Handle{}, descendantAdmissionClosed(childID)
		}
		return agent.Handle{}, createErr
	}
	return handle, nil
}

func (owner *Service) resume(
	requestContext context.Context,
	parentAgent agent.Agent,
	childID session.SessionID,
) (agent.Handle, string, error) {
	inspection, inspectErr := owner.dependencies.Persistence.Inspect(
		requestContext,
		childID,
	)
	if inspectErr != nil {
		return agent.Handle{}, "", &subagent.Error{
			Code:    subagent.ErrorNotResumable,
			Message: fmt.Sprintf("subagent %q is unavailable", childID),
			Cause:   inspectErr,
		}
	}
	if inspection.Header.ParentSession == nil ||
		*inspection.Header.ParentSession != parentAgent.ID() ||
		!owner.dependencies.Agents.Contains(parentAgent) {
		return agent.Handle{}, "", unauthorized(
			fmt.Sprintf("subagent %q belongs to another parent Session", childID),
		)
	}
	suffixStart := int64(0)
	if inspection.Header.SeedLength != nil {
		suffixStart = *inspection.Header.SeedLength
	}
	descriptorValue, found, foldErr := subagent.FoldDescriptor(
		inspection.Events[suffixStart:],
	)
	if foldErr != nil || !found {
		return agent.Handle{}, "", &subagent.Error{
			Code: subagent.ErrorNotResumable,
			Message: fmt.Sprintf(
				"subagent %q has no Continuable descriptor",
				childID,
			),
			Cause: foldErr,
		}
	}
	descriptor, matches := descriptorValue.(subagent.ContinuableDescriptor)
	if !matches {
		return agent.Handle{}, "", &subagent.Error{
			Code:    subagent.ErrorNotResumable,
			Message: fmt.Sprintf("subagent %q is not Continuable", childID),
		}
	}
	initiatedContext, contextErr := agent.WithInitiator(
		requestContext,
		parentAgent,
	)
	if contextErr != nil {
		return agent.Handle{}, "", contextErr
	}
	handle, resumeErr := owner.dependencies.Constructor.Resume(
		initiatedContext,
		agent.ResumeOptions{
			SessionID: childID,
			AgentOptions: agent.Options{
				Provider: stringValue(descriptor.AgentProvider),
				Model:    stringValue(descriptor.AgentModel),
			},
			Provisioner: owner.provisioner(
				scopePolicy{
					childID:    childID,
					parentID:   parentAgent.ID(),
					descriptor: descriptor,
					fresh:      false,
				},
			),
			RuntimeParent: parentAgent,
		},
	)
	if resumeErr != nil {
		if errors.Is(resumeErr, agent.ErrDescendantAdmissionClosed) {
			return agent.Handle{}, "", descendantAdmissionClosed(childID)
		}
		return agent.Handle{}, "", &subagent.Error{
			Code:    subagent.ErrorNotResumable,
			Message: fmt.Sprintf("subagent %q is unavailable", childID),
			Cause:   resumeErr,
		}
	}
	return handle, descriptor.Provider, nil
}

func (owner *Service) publish(
	handle agent.Handle,
	parentAgent agent.Agent,
	seedBuilder string,
	slot *childSlot,
) (*currentExecution, error) {
	runID, identityErr := sharedexecution.NewRunID()
	if identityErr != nil {
		return nil, identityErr
	}
	terminator := &executionTerminator{
		owner:       owner,
		handle:      handle,
		parent:      parentAgent,
		seedBuilder: seedBuilder,
		runID:       runID,
		boundary:    handle.Subject.SessionValue().Seq(),
	}
	running, executionErr := sharedexecution.New(
		runID,
		handle.Subject.ID(),
		terminator,
	)
	if executionErr != nil {
		return nil, executionErr
	}
	if activationErr := running.Activate(); activationErr != nil {
		return nil, activationErr
	}
	current := &currentExecution{
		running:    running,
		terminator: terminator,
		slot:       slot,
		wake:       make(chan struct{}),
	}
	terminator.current = current
	if publishErr := owner.dependencies.Executions.Publish(
		sharedexecution.Entry{
			Execution: running,
			Mode:      subagent.ModeContinuable,
			Parent:    parentAgent,
			Subject:   handle.Subject,
			Closing:   handle.ClosingSignal(),
		},
	); publishErr != nil {
		return nil, publishErr
	}
	if owner.dependencies.Lifecycle != nil {
		owner.dependencies.Lifecycle.Started(
			parentAgent,
			subagent.Started{
				RunID:    runID,
				Provider: seedBuilder,
				ID:       handle.Subject.ID(),
				Local:    true,
			},
		)
	}
	return current, nil
}

func (owner *Service) assertAvailable(
	requestContext context.Context,
	childID session.SessionID,
	checkPersistence bool,
) error {
	if _, found := owner.dependencies.Agents.Get(childID); found {
		return duplicateChild(childID)
	}
	if _, found := owner.dependencies.Sessions.Get(childID); found {
		return duplicateChild(childID)
	}
	if checkPersistence {
		snapshots, listErr := owner.dependencies.Persistence.ListSnapshots(
			requestContext,
		)
		if listErr != nil {
			return listErr
		}
		for _, snapshot := range snapshots {
			if snapshot.Header.ID == childID {
				return duplicateChild(childID)
			}
		}
	}
	return nil
}

func requestedChildID(
	requested *session.SessionID,
) (session.SessionID, error) {
	if requested != nil {
		if *requested == "" {
			return "", errors.New("subagent: Continuable child ID is empty")
		}
		return *requested, nil
	}
	return sharedexecution.NewChildID()
}

func continuableDescriptor(
	seedBuilder string,
	label string,
	request subagent.ChildRequest,
) (subagent.ContinuableDescriptor, error) {
	if request.AgentOptions == nil {
		return subagent.ContinuableDescriptor{}, errors.New(
			"subagent: Continuable descriptor requires Agent options",
		)
	}
	descriptorData, snapshotErr := subagent.SnapshotDescriptor(
		subagent.ContinuableDescriptor{
			Provider:      seedBuilder,
			Label:         label,
			AgentProvider: stringPointer(request.AgentOptions.Provider),
			AgentModel:    stringPointer(request.AgentOptions.Model),
			Persona:       request.Persona,
			ToolFilter:    request.ToolFilter,
		},
	)
	if snapshotErr != nil {
		return subagent.ContinuableDescriptor{}, snapshotErr
	}
	descriptor, matches := descriptorData.DescriptorValue().(subagent.ContinuableDescriptor)
	if !matches {
		return subagent.ContinuableDescriptor{}, errors.New(
			"subagent: Continuable descriptor snapshot changed variant",
		)
	}
	return descriptor, nil
}

func descriptorSeed(
	childID session.SessionID,
	builderSeed []session.Event,
	descriptor subagent.ContinuableDescriptor,
) ([]session.Event, error) {
	staged, stageErr := session.New(
		childID,
		session.CreateOptions{
			Seed: builderSeed,
		},
	)
	if stageErr != nil {
		return nil, stageErr
	}
	descriptorData, snapshotErr := subagent.SnapshotDescriptor(descriptor)
	if snapshotErr != nil {
		return nil, snapshotErr
	}
	draft, appendErr := session.NewEventDraft(
		subagent.DescriptorEvent,
		descriptorData,
	)
	if appendErr != nil {
		return nil, appendErr
	}
	if _, appendErr := staged.Commit(
		context.Background(),
		session.Batch(draft),
	); appendErr != nil {
		return nil, appendErr
	}
	return staged.Events(), nil
}

func checkContext(requestContext context.Context, operation string) error {
	if requestContext == nil {
		return errors.New("subagent: " + operation + " context is nil")
	}
	return requestContext.Err()
}

func duplicateChild(childID session.SessionID) error {
	return &subagent.Error{
		Code:    subagent.ErrorDuplicateChild,
		Message: fmt.Sprintf("subagent %q already exists", childID),
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

func stringPointer(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
