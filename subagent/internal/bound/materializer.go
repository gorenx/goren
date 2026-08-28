package bound

import (
	"context"
	"errors"
	"fmt"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agent/scopedplugin"
	"github.com/gorenx/goren/approval"
	"github.com/gorenx/goren/session"
	sesspersist "github.com/gorenx/goren/session/persistence"
	"github.com/gorenx/goren/subagent"
	boundcontract "github.com/gorenx/goren/subagent/bound"
	"github.com/gorenx/goren/subagent/internal/childpolicy"
	"github.com/gorenx/goren/subagent/internal/lineage"
	subagentprojection "github.com/gorenx/goren/subagent/internal/projection"
	"github.com/gorenx/goren/subagent/internal/seedbuilder"
)

// materializer owns Bound child construction, restoration, frozen context,
// descriptor validation, and exact Scope provisioning.
type materializer struct {
	constructor      agent.Constructor
	persistence      sesspersist.Persistence
	delegation       approval.DelegationPolicy
	commonExtensions agent.Provisioner
	extensions       Extensions
}

// childConfiguration is the complete constructor input derived from one
// effective Definition for the current parent lineage.
type childConfiguration struct {
	options     agent.Options
	provisioner agent.Provisioner
}

func (factory *materializer) materialize(
	requestContext context.Context,
	parentAgent agent.Agent,
	bindingValue subagentprojection.BoundBinding,
	definitionValue boundcontract.Definition,
) (agent.Handle, error) {
	inspection, err := factory.persistence.Inspect(
		requestContext,
		bindingValue.ChildSessionID,
	)
	if err == nil {
		return factory.resume(
			requestContext,
			parentAgent,
			bindingValue,
			definitionValue,
			inspection,
		)
	}
	var notFound *sesspersist.NotFoundError
	if !errors.As(err, &notFound) {
		return agent.Handle{}, err
	}
	return factory.create(
		requestContext,
		parentAgent,
		bindingValue,
		definitionValue,
	)
}

func (factory *materializer) create(
	requestContext context.Context,
	parentAgent agent.Agent,
	bindingValue subagentprojection.BoundBinding,
	definitionValue boundcontract.Definition,
) (agent.Handle, error) {
	parentEvents := parentAgent.SessionValue().Events()
	if bindingValue.ContextNextSeq < 0 ||
		bindingValue.ContextNextSeq > int64(len(parentEvents)) {
		return agent.Handle{}, errors.New(
			"subagent: Bound Binding context prefix is invalid",
		)
	}
	contextPrefix := parentEvents[:bindingValue.ContextNextSeq]
	seedValue := subagent.NewSessionSeed(contextPrefix)
	seed, err := seedbuilder.AppendDescriptor(
		bindingValue.ChildSessionID,
		seedValue.EventPrefix(),
		subagent.BoundDescriptor{
			Name: bindingValue.Name,
		},
	)
	if err != nil {
		return agent.Handle{}, err
	}
	childLineage, err := lineage.From(parentAgent, definitionValue.MaxDepth)
	if err != nil {
		return agent.Handle{}, err
	}
	configuration, err := factory.configure(
		definitionValue,
		childLineage.AgentOptions(definitionValue.AgentOptions),
		true,
	)
	if err != nil {
		return agent.Handle{}, err
	}
	initiated, err := agent.WithInitiator(requestContext, parentAgent)
	if err != nil {
		return agent.Handle{}, err
	}
	handle, err := factory.constructor.Create(
		initiated,
		agent.CreateOptions{
			SessionID: bindingValue.ChildSessionID,
			Metadata: childLineage.Metadata(
				bindingValue.ContextNextSeq,
			),
			Seed:          seed,
			AgentOptions:  configuration.options,
			Provisioner:   configuration.provisioner,
			RuntimeParent: parentAgent,
		},
	)
	if errors.Is(err, agent.ErrDescendantAdmissionClosed) {
		return agent.Handle{}, descendantAdmissionClosed(
			bindingValue.ChildSessionID,
		)
	}
	return handle, err
}

func (factory *materializer) resume(
	requestContext context.Context,
	parentAgent agent.Agent,
	bindingValue subagentprojection.BoundBinding,
	definitionValue boundcontract.Definition,
	inspection sesspersist.Inspection,
) (agent.Handle, error) {
	if inspection.Header.ParentSession == nil ||
		*inspection.Header.ParentSession != parentAgent.ID() ||
		inspection.Header.Origin != session.OriginSubagent {
		return agent.Handle{}, unauthorizedChild(bindingValue.ChildSessionID)
	}
	seedLength := int64(0)
	if inspection.Header.SeedLength != nil {
		seedLength = *inspection.Header.SeedLength
	}
	if seedLength != bindingValue.ContextNextSeq ||
		bindingValue.ContextNextSeq < 0 ||
		bindingValue.ContextNextSeq > int64(len(inspection.Events)) {
		return agent.Handle{}, notResumable(
			bindingValue.ChildSessionID,
			"does not match its frozen Bound context",
			nil,
		)
	}
	descriptorValue, found, err := subagent.FoldDescriptor(
		inspection.Events[bindingValue.ContextNextSeq:],
	)
	if err != nil || !found {
		return agent.Handle{}, notResumable(
			bindingValue.ChildSessionID,
			"has no Bound descriptor",
			err,
		)
	}
	descriptor, matches := descriptorValue.(subagent.BoundDescriptor)
	if !matches || descriptor.Name != bindingValue.Name {
		return agent.Handle{}, notResumable(
			bindingValue.ChildSessionID,
			"does not match its Bound Binding",
			nil,
		)
	}
	childLineage, err := lineage.From(parentAgent, definitionValue.MaxDepth)
	if err != nil {
		return agent.Handle{}, err
	}
	configuration, err := factory.configure(
		definitionValue,
		childLineage.AgentOptions(definitionValue.AgentOptions),
		false,
	)
	if err != nil {
		return agent.Handle{}, err
	}
	initiated, err := agent.WithInitiator(requestContext, parentAgent)
	if err != nil {
		return agent.Handle{}, err
	}
	handle, err := factory.constructor.Resume(
		initiated,
		agent.ResumeOptions{
			SessionID:     bindingValue.ChildSessionID,
			AgentOptions:  configuration.options,
			Provisioner:   configuration.provisioner,
			RuntimeParent: parentAgent,
		},
	)
	if errors.Is(err, agent.ErrDescendantAdmissionClosed) {
		return agent.Handle{}, descendantAdmissionClosed(
			bindingValue.ChildSessionID,
		)
	}
	if err != nil {
		return agent.Handle{}, notResumable(
			bindingValue.ChildSessionID,
			"is unavailable",
			err,
		)
	}
	return handle, nil
}

func (factory *materializer) requireDependencies() error {
	if factory == nil || factory.constructor == nil ||
		factory.persistence == nil || factory.extensions == nil {
		return unavailableDependency("materialization dependencies")
	}
	return nil
}

func (factory *materializer) configure(
	definitionValue boundcontract.Definition,
	options agent.Options,
	fresh bool,
) (childConfiguration, error) {
	effective, err := boundcontract.NewDefinition(
		boundcontract.Draft{
			Name:            definitionValue.Name,
			Enabled:         definitionValue.Enabled,
			SystemPrompt:    definitionValue.SystemPrompt,
			AgentOptions:    &options,
			MaxDepth:        definitionValue.MaxDepth,
			ToolRestriction: definitionValue.ToolRestriction,
			Extensions:      definitionValue.Extensions,
		},
		definitionValue.Revision,
	)
	if err != nil {
		return childConfiguration{}, fmt.Errorf(
			"subagent: invalid effective Bound Definition: %w",
			err,
		)
	}
	definitionProvisioner, err := factory.provisioner(effective, fresh)
	if err != nil {
		return childConfiguration{}, err
	}
	return childConfiguration{
		options: *effective.AgentOptions,
		provisioner: agent.ComposeProvisioners(
			definitionProvisioner,
			newAppliedProvisioner(effective),
		),
	}, nil
}

func (factory *materializer) provisioner(
	definitionValue boundcontract.Definition,
	fresh bool,
) (agent.Provisioner, error) {
	if factory == nil || factory.extensions == nil {
		return nil, errors.New(
			"subagent: Bound Extension selection is unavailable",
		)
	}
	selectedExtensions, err := factory.extensions.Provision(
		definitionValue.Extensions,
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
			SystemPrompt:    &definitionValue.SystemPrompt,
			ToolRestriction: definitionValue.ToolRestriction,
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
