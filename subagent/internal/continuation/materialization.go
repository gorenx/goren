package continuation

import (
	"context"
	"fmt"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/subagent/internal/childscope"
	"github.com/gorenx/goren/subagent/internal/lineage"
)

func (owner *Manager) create(
	requestContext context.Context,
	childID session.SessionID,
	providerName string,
	descriptor subagent.ContinuableDescriptor,
	requestSnapshot subagent.ContinuableRequest,
	childLineage lineage.Lineage,
	seed []session.Event,
	lineageSeedLength int64,
) (*Activation, error) {
	initiatedContext, contextErr := agent.WithInitiator(
		requestContext,
		requestSnapshot.Parent,
	)
	if contextErr != nil {
		return nil, contextErr
	}
	handle, createErr := owner.dependencies.Agents.Create(
		owner.dependencies.Custody.Bind(initiatedContext),
		agent.CreateOptions{
			SessionID:    childID,
			Metadata:     childLineage.Metadata(lineageSeedLength),
			Seed:         seed,
			AgentOptions: *requestSnapshot.AgentOptions,
			Provisioner: owner.dependencies.ScopeBuilder.Provisioner(
				childscope.ContinuableInput{
					ChildID:    childID,
					ParentID:   requestSnapshot.Parent.ID(),
					Descriptor: descriptor,
					Fresh:      true,
				},
			),
		},
	)
	if createErr != nil {
		return nil, createErr
	}
	return owner.publish(requestContext, handle, providerName, requestSnapshot.Parent)
}

func (owner *Manager) resume(
	requestContext context.Context,
	parentAgent agent.Agent,
	childID session.SessionID,
) (*Activation, error) {
	if owner.dependencies.Persistence == nil {
		return nil, &subagent.Error{
			Code:    subagent.ErrorPersistenceUnavailable,
			Message: "continuable subagents require Session persistence",
		}
	}
	inspection, inspectErr := owner.dependencies.Persistence.Inspect(
		requestContext,
		childID,
	)
	if inspectErr != nil {
		return nil, &subagent.Error{
			Code:    subagent.ErrorNotResumable,
			Message: fmt.Sprintf("subagent %q is unavailable", childID),
			Cause:   inspectErr,
		}
	}
	if inspection.Header.ParentSession == nil ||
		*inspection.Header.ParentSession != parentAgent.ID() ||
		!owner.dependencies.Agents.Contains(parentAgent) {
		return nil, unauthorized(
			fmt.Sprintf("subagent %q belongs to another parent Session", childID),
		)
	}
	suffixStart := int64(0)
	if inspection.Header.SeedLength != nil {
		suffixStart = *inspection.Header.SeedLength
	}
	identity, found, foldErr := subagent.FoldDescriptor(
		inspection.Events[suffixStart:],
	)
	if foldErr != nil || !found {
		return nil, &subagent.Error{
			Code:    subagent.ErrorNotResumable,
			Message: fmt.Sprintf("subagent %q has no supported continuation state", childID),
			Cause:   foldErr,
		}
	}
	continuableIdentity, matches := identity.(subagent.ContinuableDescriptor)
	if !matches {
		return nil, &subagent.Error{
			Code:    subagent.ErrorNotResumable,
			Message: fmt.Sprintf("subagent %q is not continuable", childID),
		}
	}
	childOptions := agent.Options{
		Provider:      stringValue(continuableIdentity.AgentProvider),
		Model:         stringValue(continuableIdentity.AgentModel),
		SubagentDepth: inspection.Header.DelegationDepth,
	}
	initiatedContext, contextErr := agent.WithInitiator(requestContext, parentAgent)
	if contextErr != nil {
		return nil, contextErr
	}
	handle, resumeErr := owner.dependencies.Agents.Resume(
		owner.dependencies.Custody.Bind(initiatedContext),
		agent.ResumeOptions{
			SessionID:    childID,
			AgentOptions: childOptions,
			Provisioner: owner.dependencies.ScopeBuilder.Provisioner(
				childscope.ContinuableInput{
					ChildID:    childID,
					ParentID:   parentAgent.ID(),
					Descriptor: continuableIdentity,
					Fresh:      false,
				},
			),
		},
	)
	if resumeErr != nil {
		return nil, &subagent.Error{
			Code:    subagent.ErrorNotResumable,
			Message: fmt.Sprintf("subagent %q is unavailable", childID),
			Cause:   resumeErr,
		}
	}
	return owner.publish(
		requestContext,
		handle,
		continuableIdentity.Provider,
		parentAgent,
	)
}

func (owner *Manager) publish(
	requestContext context.Context,
	handle agent.Handle,
	providerName string,
	parentAgent agent.Agent,
) (*Activation, error) {
	runID, identityErr := newRunID()
	if identityErr != nil {
		_ = handle.Dispose(context.Background())
		return nil, identityErr
	}
	epoch := &Activation{
		childID:       handle.Subject.ID(),
		parentID:      parentAgent.ID(),
		providerName:  providerName,
		handle:        handle,
		ancestry:      owner.buildLineage(parentAgent, handle.Subject),
		ownedChildren: make(map[session.SessionID]struct{}),
		accepted:      make(map[llm.MessageID]struct{}),
		wake:          make(chan struct{}),
		runID:         runID,
		boundary:      handle.Subject.SessionValue().Seq(),
	}
	owner.residency.mutex.Lock()
	closing := owner.closingForLocked(epoch.ancestry)
	if closing || owner.residency.activations[epoch.childID] != nil {
		owner.residency.mutex.Unlock()
		_ = handle.Dispose(context.Background())
		if closing {
			return nil, &subagent.Error{
				Code:    subagent.ErrorDraining,
				Message: "continuable subagent materialization lost the drain cutoff",
			}
		}
		return nil, &subagent.Error{
			Code:    subagent.ErrorDuplicateChild,
			Message: fmt.Sprintf("subagent %q already exists", epoch.childID),
		}
	}
	owner.residency.activations[epoch.childID] = epoch
	if parentEpoch := owner.residency.activations[parentAgent.ID()]; parentEpoch != nil {
		parentEpoch.ownedChildren[epoch.childID] = struct{}{}
		wake(parentEpoch)
	}
	owner.residency.mutex.Unlock()
	if owner.dependencies.Lifecycle != nil {
		owner.dependencies.Lifecycle.Started(
			parentAgent,
			subagent.Started{
				RunID:    runID,
				Provider: providerName,
				ID:       epoch.childID,
				Local:    true,
			},
		)
	}
	return epoch, nil
}
