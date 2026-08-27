package continuable

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/subagent/internal/lineage"
	"github.com/gorenx/goren/subagent/internal/seedbuilder"
)

// Start creates one durable child and returns its first common Execution.
func (owner *Service) Start(
	ctx context.Context,
	command subagent.ContinuableStartCommand,
) (subagent.Execution, error) {
	if contextErr := checkContext(ctx, "Continuable Start"); contextErr != nil {
		return nil, contextErr
	}
	requestSnapshot, snapshotErr := command.Snapshot()
	if snapshotErr != nil {
		return nil, snapshotErr
	}
	if authorizationErr := owner.authorizeParent(requestSnapshot.Parent); authorizationErr != nil {
		return nil, authorizationErr
	}
	seedBuilderName := command.SeedBuilderName()
	childLineage, lineageErr := lineage.From(
		requestSnapshot.Parent,
		requestSnapshot.MaxDepth,
	)
	if lineageErr != nil {
		return nil, lineageErr
	}
	resolvedOptions := childLineage.AgentOptions(requestSnapshot.AgentOptions)
	requestSnapshot.AgentOptions = &resolvedOptions
	descriptor, descriptorErr := continuableDescriptor(
		seedBuilderName,
		command.Label(),
		requestSnapshot,
	)
	if descriptorErr != nil {
		return nil, descriptorErr
	}
	requestedID := command.RequestedChildID()
	childID, childIDErr := requestedChildID(requestedID)
	if childIDErr != nil {
		return nil, childIDErr
	}
	builder, found := owner.dependencies.SeedBuilders.Find(
		seedBuilderName,
	)
	if !found {
		return nil, noSeedBuilder(seedBuilderName)
	}
	parentSession := requestSnapshot.Parent.SessionValue()
	if parentSession == nil {
		return nil, errors.New("subagent: parent Session is unavailable")
	}
	seedValue, seedErr := builder.BuildSeed(
		ctx,
		parentSession.Events(),
	)
	if seedErr != nil {
		return nil, seedErr
	}
	builderSeed := seedValue.EventPrefix()
	seed, seedErr := seedbuilder.AppendDescriptor(
		childID,
		builderSeed,
		descriptor,
	)
	if seedErr != nil {
		return nil, seedErr
	}
	slot := owner.acquireSlot(childID)
	defer owner.releaseSlot(childID, slot)
	slot.mutex.Lock()
	defer slot.mutex.Unlock()
	if requestErr := ctx.Err(); requestErr != nil {
		return nil, requestErr
	}
	if authorizationErr := owner.authorizeParent(requestSnapshot.Parent); authorizationErr != nil {
		return nil, authorizationErr
	}
	if slot.current != nil {
		return nil, duplicateChild(childID)
	}
	if availabilityErr := owner.assertAvailable(
		childID,
	); availabilityErr != nil {
		return nil, availabilityErr
	}
	if requestedID != nil {
		if availabilityErr := owner.assertPersistedAvailable(
			ctx,
			childID,
		); availabilityErr != nil {
			return nil, availabilityErr
		}
	}
	handle, createErr := owner.create(
		ctx,
		childID,
		descriptor,
		requestSnapshot,
		childLineage,
		seed,
		int64(len(builderSeed)),
	)
	if createErr != nil {
		return nil, createErr
	}
	prompt, messageErr := agentmessage.NewUserMessage(agentmessage.UserMessageInput{
		Content: requestSnapshot.Prompt,
		Source:  agentmessage.UserMessageSource{},
	})
	if messageErr != nil {
		return nil, errors.Join(
			messageErr,
			handle.Dispose(context.WithoutCancel(ctx)),
		)
	}
	if submitErr := handle.Subject.Followup(prompt); submitErr != nil {
		return nil, errors.Join(
			submitErr,
			handle.Dispose(context.WithoutCancel(ctx)),
		)
	}
	current, publishErr := owner.publish(
		handle,
		requestSnapshot.Parent,
		seedBuilderName,
		slot,
	)
	if publishErr != nil {
		return nil, errors.Join(
			publishErr,
			handle.Dispose(context.WithoutCancel(ctx)),
		)
	}
	owner.watch(current)
	return current.running, nil
}
