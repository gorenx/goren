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
		childEnvironment: childEnvironment{
			plugins:    scopedplugin.MountPlugins(instances...),
			extensions: builder.extensionProvisioner(),
		},
		structured: structured,
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
	extensions := builder.extensionProvisioner()
	if policies == nil && extensions == nil {
		return nil
	}
	return &childEnvironment{
		plugins:    policies,
		extensions: extensions,
	}
}

func (builder *environmentBuilder) extensionProvisioner() agent.Provisioner {
	if builder.extensions == nil {
		return nil
	}
	return extensionregistry.NewProvisioner(builder.extensions)
}

// oneShotEnvironment is one exact OneShot child's Plugin-backed environment.
type oneShotEnvironment struct {
	childEnvironment
	structured *structuredOutput
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

// childEnvironment applies child-local Plugins before installing registered
// Extensions. Plugin mounts transfer to the Agent Scope; Extension
// provisioning remains the publication transaction returned to the Agent
// constructor.
type childEnvironment struct {
	plugins    agent.Provisioner
	extensions agent.Provisioner
}

func (environment *childEnvironment) Provision(
	requestContext context.Context,
	target agent.Scope,
) (agent.Provisioning, error) {
	if environment.plugins != nil {
		if _, err := environment.plugins.Provision(
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
var _ agent.Provisioner = (*childEnvironment)(nil)
