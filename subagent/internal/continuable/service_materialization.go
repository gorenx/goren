package continuable

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
	sesspersist "github.com/gorenx/goren/session/persistence"
	"github.com/gorenx/goren/subagent"
	sharedexecution "github.com/gorenx/goren/subagent/internal/execution"
	"github.com/gorenx/goren/subagent/internal/lineage"
)

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

func (owner *Service) create(
	ctx context.Context,
	childID session.SessionID,
	descriptor subagent.ContinuableDescriptor,
	request subagent.ContinuableOptions,
	childLineage lineage.Lineage,
	seed []session.Event,
	lineageSeedLength int64,
) (agent.Handle, error) {
	initiatedContext, contextErr := agent.WithInitiator(
		ctx,
		request.Parent,
	)
	if contextErr != nil {
		return agent.Handle{}, contextErr
	}
	handle, createErr := owner.dependencies.Constructor.Create(
		initiatedContext,
		agent.CreateOptions{
			SessionID:     childID,
			Metadata:      childLineage.Metadata(lineageSeedLength),
			Seed:          seed,
			AgentOptions:  *request.AgentOptions,
			Provisioner:   owner.provisioner(descriptor, true),
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
	ctx context.Context,
	parentAgent agent.Agent,
	childID session.SessionID,
) (agent.Handle, string, error) {
	inspection, inspectErr := owner.dependencies.Persistence.Inspect(
		ctx,
		childID,
	)
	if inspectErr != nil {
		return agent.Handle{}, "", notResumable(
			childID,
			"is unavailable",
			inspectErr,
		)
	}
	if inspection.Header.ParentSession == nil ||
		*inspection.Header.ParentSession != parentAgent.ID() ||
		!owner.dependencies.Agents.Contains(parentAgent) {
		return agent.Handle{}, "", unauthorizedChild(childID)
	}
	suffixStart := int64(0)
	if inspection.Header.SeedLength != nil {
		suffixStart = *inspection.Header.SeedLength
	}
	descriptorValue, found, foldErr := subagent.FoldDescriptor(
		inspection.Events[suffixStart:],
	)
	if foldErr != nil || !found {
		return agent.Handle{}, "", notResumable(
			childID,
			"has no Continuable descriptor",
			foldErr,
		)
	}
	descriptor, matches := descriptorValue.(subagent.ContinuableDescriptor)
	if !matches {
		return agent.Handle{}, "", notResumable(
			childID,
			"is not Continuable",
			nil,
		)
	}
	initiatedContext, contextErr := agent.WithInitiator(
		ctx,
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
			Provisioner:   owner.provisioner(descriptor, false),
			RuntimeParent: parentAgent,
		},
	)
	if resumeErr != nil {
		if errors.Is(resumeErr, agent.ErrDescendantAdmissionClosed) {
			return agent.Handle{}, "", descendantAdmissionClosed(childID)
		}
		return agent.Handle{}, "", notResumable(
			childID,
			"is unavailable",
			resumeErr,
		)
	}
	return handle, descriptor.Provider, nil
}

func (owner *Service) assertAvailable(childID session.SessionID) error {
	if _, found := owner.dependencies.Agents.Get(childID); found {
		return duplicateChild(childID)
	}
	if _, found := owner.dependencies.Sessions.Get(childID); found {
		return duplicateChild(childID)
	}
	return nil
}

func (owner *Service) assertPersistedAvailable(
	ctx context.Context,
	childID session.SessionID,
) error {
	var cursor *sesspersist.SessionCursor
	for {
		page, listErr := owner.dependencies.Persistence.ListSnapshots(
			ctx,
			sesspersist.SessionPage{
				Cursor: cursor,
				Limit:  256,
			},
		)
		if listErr != nil {
			return listErr
		}
		for _, snapshot := range page.Snapshots {
			if snapshot.Header.ID == childID {
				return duplicateChild(childID)
			}
		}
		if page.NextCursor == nil {
			return nil
		}
		cursor = page.NextCursor
	}
}
