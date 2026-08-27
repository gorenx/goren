package bound

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/gorenx/goren/agent"
	sesspersist "github.com/gorenx/goren/session/persistence"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/subagent/internal/lineage"
	subagentprojection "github.com/gorenx/goren/subagent/internal/projection"
	"github.com/gorenx/goren/subagent/internal/seedbuilder"
)

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
