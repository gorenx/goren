package continuable

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agent/scopedplugin"
	"github.com/gorenx/goren/approval"
	"github.com/gorenx/goren/session"
	sesspersist "github.com/gorenx/goren/session/persistence"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/subagent/internal/childpolicy"
	sharedexecution "github.com/gorenx/goren/subagent/internal/execution"
	"github.com/gorenx/goren/subagent/internal/lineage"
)

// materializer owns Continuable child construction and restoration policy,
// including exact Agent options, descriptor checks, and Scope provisioning.
type materializer struct {
	agents      agent.Registry
	constructor agent.Constructor
	sessions    session.LiveStore
	persistence sesspersist.Persistence
	delegation  approval.DelegationPolicy
	extensions  agent.Provisioner
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

func (factory *materializer) create(
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
	handle, createErr := factory.constructor.Create(
		initiatedContext,
		agent.CreateOptions{
			SessionID:     childID,
			Metadata:      childLineage.Metadata(lineageSeedLength),
			Seed:          seed,
			AgentOptions:  *request.AgentOptions,
			Provisioner:   factory.provisioner(descriptor, true),
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

func (factory *materializer) resume(
	ctx context.Context,
	parentAgent agent.Agent,
	childID session.SessionID,
) (agent.Handle, string, error) {
	inspection, inspectErr := factory.persistence.Inspect(
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
		!factory.agents.Contains(parentAgent) {
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
	handle, resumeErr := factory.constructor.Resume(
		initiatedContext,
		agent.ResumeOptions{
			SessionID: childID,
			AgentOptions: agent.Options{
				Provider: stringValue(descriptor.AgentProvider),
				Model:    stringValue(descriptor.AgentModel),
			},
			Provisioner:   factory.provisioner(descriptor, false),
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

func (factory *materializer) assertAvailable(childID session.SessionID) error {
	if _, found := factory.agents.Get(childID); found {
		return duplicateChild(childID)
	}
	if _, found := factory.sessions.Get(childID); found {
		return duplicateChild(childID)
	}
	return nil
}

func (factory *materializer) assertPersistedAvailable(
	ctx context.Context,
	childID session.SessionID,
) error {
	var cursor *sesspersist.SessionCursor
	for {
		page, listErr := factory.persistence.ListSnapshots(
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

// provisioner resolves the child-scoped effects recorded by the Continuable
// descriptor. Only a fresh child receives the parent delegation policy.
func (factory *materializer) provisioner(
	descriptor subagent.ContinuableDescriptor,
	fresh bool,
) agent.Provisioner {
	instances := childpolicy.Plugins(
		childpolicy.PolicySet{
			Persona:         descriptor.Persona,
			ToolRestriction: descriptor.ToolFilter,
		},
	)
	var policies agent.Provisioner
	if len(instances) != 0 {
		policies = scopedplugin.MountPlugins(instances...)
	}
	var delegation agent.Provisioner
	if fresh {
		delegation = childpolicy.DelegationSeed(factory.delegation)
	}
	return agent.ComposeProvisioners(
		delegation,
		policies,
		factory.extensions,
	)
}

func continuableDescriptor(
	seedBuilder string,
	label string,
	request subagent.ContinuableOptions,
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
