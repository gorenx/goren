package plugin

import (
	"context"
	"encoding/json"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agent/scopedplugin"
	"github.com/gorenx/goren/approval"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/subagent/internal/childpolicy"
	"github.com/gorenx/goren/subagent/internal/continuable"
	extensionregistry "github.com/gorenx/goren/subagent/internal/extension"
	"github.com/gorenx/goren/subagent/internal/oneshot"
)

// environmentBuilder translates mode-owned environment requests into
// child-scoped Plugin adapters.
type environmentBuilder struct {
	delegation approval.DelegationPolicy
	extensions *extensionregistry.Registry
}

func (builder *environmentBuilder) Build(
	options oneshot.ChildEnvironmentOptions,
) oneshot.ChildEnvironment {
	instances := childpolicy.Plugins(
		childpolicy.PolicySet{
			Delegation:      builder.delegation,
			Persona:         options.Persona,
			ToolRestriction: options.ToolFilter,
		},
	)
	instances = append(
		instances,
		&descriptorAppender{
			descriptor: options.Descriptor,
		},
	)
	var structured *structuredOutput
	if len(options.OutputSchema) != 0 {
		structured = newStructuredOutput(options.OutputSchema)
		instances = append(instances, structured)
	}
	return &oneShotEnvironment{
		provisioner: scopedplugin.MountPlugins(instances...),
		structured:  structured,
	}
}

func (builder *environmentBuilder) BuildForCreation(
	descriptor subagent.ContinuableDescriptor,
) agent.Provisioner {
	return builder.buildContinuable(descriptor, builder.delegation)
}

func (builder *environmentBuilder) BuildForResume(
	descriptor subagent.ContinuableDescriptor,
) agent.Provisioner {
	return builder.buildContinuable(descriptor, nil)
}

func (builder *environmentBuilder) buildContinuable(
	descriptor subagent.ContinuableDescriptor,
	delegation approval.DelegationPolicy,
) agent.Provisioner {
	instances := childpolicy.Plugins(
		childpolicy.PolicySet{
			Delegation:      delegation,
			Persona:         descriptor.Persona,
			ToolRestriction: descriptor.ToolFilter,
		},
	)
	var policies agent.Provisioner
	if len(instances) != 0 {
		policies = scopedplugin.MountPlugins(instances...)
	}
	var extensions agent.Provisioner
	if builder.extensions != nil {
		extensions = extensionregistry.NewProvisioner(builder.extensions)
	}
	if policies == nil && extensions == nil {
		return nil
	}
	return &continuableEnvironment{
		policies:   policies,
		extensions: extensions,
	}
}

// oneShotEnvironment is one exact OneShot child's Plugin-backed environment.
type oneShotEnvironment struct {
	provisioner agent.Provisioner
	structured  *structuredOutput
}

func (environment *oneShotEnvironment) Provision(
	requestContext context.Context,
	target agent.Scope,
) (agent.Provisioning, error) {
	return environment.provisioner.Provision(requestContext, target)
}

func (environment *oneShotEnvironment) StructuredOutput() (
	json.RawMessage,
	bool,
) {
	if environment.structured == nil {
		return nil, false
	}
	return environment.structured.Captured(), true
}

// continuableEnvironment applies child policy before installing
// Continuable-only Extensions. Policy mounts transfer to the Agent Scope;
// Extension provisioning remains the publication transaction returned to the
// Agent constructor.
type continuableEnvironment struct {
	policies   agent.Provisioner
	extensions agent.Provisioner
}

func (environment *continuableEnvironment) Provision(
	requestContext context.Context,
	target agent.Scope,
) (agent.Provisioning, error) {
	if environment.policies != nil {
		if _, err := environment.policies.Provision(
			requestContext,
			target,
		); err != nil {
			return nil, err
		}
	}
	if environment.extensions == nil {
		return nil, nil
	}
	return environment.extensions.Provision(requestContext, target)
}

var _ oneshot.EnvironmentBuilder = (*environmentBuilder)(nil)
var _ continuable.EnvironmentBuilder = (*environmentBuilder)(nil)
var _ oneshot.ChildEnvironment = (*oneShotEnvironment)(nil)
var _ agent.Provisioner = (*continuableEnvironment)(nil)
