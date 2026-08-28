package bound

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agent/scopedplugin"
	"github.com/gorenx/goren/approval"
	sesspersist "github.com/gorenx/goren/session/persistence"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/subagent/internal/childpolicy"
	"github.com/gorenx/goren/subagent/internal/lineage"
	subagentprojection "github.com/gorenx/goren/subagent/internal/projection"
	"github.com/gorenx/goren/subagent/internal/seedbuilder"
	"github.com/gorenx/goren/tools"
)

// materializer owns Bound Agent construction and restoration policy. A
// boundChild selects a committed binding and serializes its use cases;
// materializer owns descriptor validation, seed construction, and scoped
// provisioning.
type materializer struct {
	constructor      agent.Constructor
	persistence      sesspersist.Persistence
	seedBuilders     subagent.SeedBuilderRegistry
	delegation       approval.DelegationPolicy
	commonExtensions agent.Provisioner
	extensions       Extensions
}

// childEpoch is one materialized Bound Agent epoch together with the recovery
// state needed before it can be published as a resident execution.
type childEpoch struct {
	handle               agent.Handle
	initialPromptPending bool
}

func (factory *materializer) materialize(
	ctx context.Context,
	parentAgent agent.Agent,
	binding subagentprojection.BoundBinding,
	config subagentprojection.BoundConfig,
) (childEpoch, error) {
	inspection, err := factory.persistence.Inspect(
		ctx,
		binding.ChildSessionID,
	)
	if err == nil {
		handle, resumeErr := factory.resume(
			ctx,
			parentAgent,
			binding,
			config,
			inspection,
		)
		if resumeErr != nil {
			return childEpoch{}, resumeErr
		}
		return childEpoch{
			handle:               handle,
			initialPromptPending: !hasSubmittedMessage(inspection),
		}, nil
	}
	var notFound *sesspersist.NotFoundError
	if !errors.As(err, &notFound) {
		return childEpoch{}, err
	}
	handle, createErr := factory.create(
		ctx,
		parentAgent,
		binding,
		config,
	)
	if createErr != nil {
		return childEpoch{}, createErr
	}
	return childEpoch{
		handle:               handle,
		initialPromptPending: true,
	}, nil
}

func (factory *materializer) create(
	ctx context.Context,
	parentAgent agent.Agent,
	binding subagentprojection.BoundBinding,
	config subagentprojection.BoundConfig,
) (agent.Handle, error) {
	builder, found := factory.seedBuilders.Find(
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
	configProvisioner, err := factory.provisioner(
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
	handle, err := factory.constructor.Create(
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

func (factory *materializer) resume(
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
	configProvisioner, err := factory.provisioner(
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
	handle, err := factory.constructor.Resume(
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

func (factory *materializer) requireDependencies() error {
	if factory.constructor == nil || factory.persistence == nil ||
		factory.seedBuilders == nil || factory.extensions == nil {
		return unavailableDependency("materialization dependencies")
	}
	return nil
}

// provisioner composes one Bound epoch from the exact parent projection
// snapshot. Only a fresh child receives the parent delegation policy.
func (factory *materializer) provisioner(
	config subagent.BoundConfigSnapshot,
	fresh bool,
) (agent.Provisioner, error) {
	if factory == nil || factory.extensions == nil {
		return nil, errors.New(
			"subagent: Bound Extension selection is unavailable",
		)
	}
	detached := cloneBoundConfig(config)
	selectedExtensions, err := factory.extensions.Provision(
		detached.Extensions,
	)
	if err != nil {
		return nil, err
	}
	var delegation approval.DelegationPolicy
	if fresh {
		delegation = factory.delegation
	}
	policyPlugins := childpolicy.Plugins(
		childpolicy.PolicySet{
			Delegation:      delegation,
			Persona:         detached.Persona,
			ToolRestriction: detached.ToolRestriction,
		},
	)
	var policies agent.Provisioner
	if len(policyPlugins) != 0 {
		policies = scopedplugin.MountPlugins(policyPlugins...)
	}
	return agent.ComposeProvisioners(
		policies,
		factory.commonExtensions,
		selectedExtensions,
	), nil
}

func hasSubmittedMessage(inspection sesspersist.Inspection) bool {
	childStart := int64(0)
	if inspection.Header.SeedLength != nil {
		childStart = *inspection.Header.SeedLength
	}
	for _, committed := range inspection.Events {
		if committed.Seq < childStart ||
			committed.Type != agent.InboxSplicedEventName {
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

func cloneBoundConfig(
	source subagent.BoundConfigSnapshot,
) subagent.BoundConfigSnapshot {
	detached := subagent.BoundConfigSnapshot{
		Enabled:    source.Enabled,
		Extensions: append([]string(nil), source.Extensions...),
	}
	if source.Persona != nil {
		persona := *source.Persona
		detached.Persona = &persona
	}
	if source.ToolRestriction != nil {
		detached.ToolRestriction = &tools.ToolRestriction{
			Allow: append([]string(nil), source.ToolRestriction.Allow...),
			Deny:  append([]string(nil), source.ToolRestriction.Deny...),
		}
	}
	return detached
}
