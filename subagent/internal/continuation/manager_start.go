package continuation

import (
	"context"
	"errors"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/subagent/internal/lineage"
)

// Start creates one durable child Activation and accepts its initial prompt.
func (owner *Manager) Start(
	requestContext context.Context,
	startSpec subagent.ContinuableStartSpec,
) (subagent.ContinuableStart, error) {
	if contextErr := checkContext(requestContext, "continuable Start"); contextErr != nil {
		return subagent.ContinuableStart{}, contextErr
	}
	if owner.dependencies.Persistence == nil {
		return subagent.ContinuableStart{}, &subagent.Error{
			Code:    subagent.ErrorPersistenceUnavailable,
			Message: "continuable subagents require Session persistence",
		}
	}
	requestSnapshot, snapshotErr := snapshotRequest(startSpec.Request)
	if snapshotErr != nil {
		return subagent.ContinuableStart{}, snapshotErr
	}
	if requestSnapshot.Parent == nil ||
		!owner.dependencies.Agents.Contains(requestSnapshot.Parent) {
		return subagent.ContinuableStart{}, unauthorized(
			"continuable Start requires the exact live parent Agent",
		)
	}
	childLineage, lineageErr := lineage.From(
		requestSnapshot.Parent,
		requestSnapshot.MaxDepth,
	)
	if lineageErr != nil {
		return subagent.ContinuableStart{}, lineageErr
	}
	resolvedOptions := childLineage.AgentOptions(requestSnapshot.AgentOptions)
	requestSnapshot.AgentOptions = &resolvedOptions
	descriptor, descriptorErr := continuableDescriptor(
		startSpec.Provider,
		startSpec.Label,
		requestSnapshot,
	)
	if descriptorErr != nil {
		return subagent.ContinuableStart{}, descriptorErr
	}
	childID := session.SessionID("")
	if startSpec.ChildID == nil {
		generatedID, generateErr := newSessionID()
		if generateErr != nil {
			return subagent.ContinuableStart{}, generateErr
		}
		childID = generatedID
	} else {
		childID = *startSpec.ChildID
	}
	if childID == "" {
		return subagent.ContinuableStart{}, errors.New(
			"subagent: continuable child id is empty",
		)
	}
	if admissionErr := owner.assertAdmitting(requestSnapshot.Parent); admissionErr != nil {
		return subagent.ContinuableStart{}, admissionErr
	}
	continuationProvider, providerErr := owner.prepareProvider(
		startSpec.Provider,
	)
	if providerErr != nil {
		return subagent.ContinuableStart{}, providerErr
	}
	prepared, prepareErr := continuationProvider.PrepareContinuable(
		requestContext,
		subagent.ContinuableCreateRequest{
			SessionID: childID,
			Parent:    requestSnapshot.Parent,
		},
	)
	if prepareErr != nil {
		return subagent.ContinuableStart{}, prepareErr
	}
	seed, seedErr := descriptorSeed(childID, prepared.Seed, descriptor)
	if seedErr != nil {
		return subagent.ContinuableStart{}, seedErr
	}
	childMutex := owner.lockFor(childID)
	childMutex.Lock()
	defer childMutex.Unlock()
	if contextErr := requestContext.Err(); contextErr != nil {
		return subagent.ContinuableStart{}, contextErr
	}
	if admissionErr := owner.assertAdmitting(requestSnapshot.Parent); admissionErr != nil {
		return subagent.ContinuableStart{}, admissionErr
	}
	if availabilityErr := owner.assertAvailable(
		requestContext,
		childID,
		startSpec.ChildID != nil,
	); availabilityErr != nil {
		return subagent.ContinuableStart{}, availabilityErr
	}
	building, admissionErr := owner.beginMaterialization(requestSnapshot.Parent)
	if admissionErr != nil {
		return subagent.ContinuableStart{}, admissionErr
	}
	defer owner.finishMaterialization(building)
	epoch, materializeErr := owner.create(
		requestContext,
		childID,
		startSpec.Provider,
		descriptor,
		requestSnapshot,
		childLineage,
		seed,
		int64(len(prepared.Seed)),
	)
	if materializeErr != nil {
		return subagent.ContinuableStart{}, materializeErr
	}
	messageID, submitErr := owner.submit(
		requestContext,
		epoch,
		requestSnapshot.Parent,
		requestSnapshot.Prompt,
		llm.UserMessageSource{
			Kind: "user",
		},
	)
	if submitErr != nil {
		_ = owner.dispose(context.Background(), epoch, subagent.StopAborted)
		return subagent.ContinuableStart{}, submitErr
	}
	owner.watch(epoch)
	return subagent.ContinuableStart{
		ChildID:   childID,
		MessageID: messageID,
	}, nil
}
