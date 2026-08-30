package oneshot

import (
	"context"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/subagent/internal/childpolicy"
)

// setup resolves the child-local contributions required by this OneShot.
func (owner *Service) setup(
	settings subagent.OneShotOptions,
	descriptor subagent.OneShotDescriptor,
) (agent.Setup, *structuredOutput) {
	policies := childpolicy.Setup(
		childpolicy.PolicySet{
			Persona:         settings.Persona,
			ToolRestriction: settings.ToolFilter,
		},
	)
	setups := []agent.Setup{
		preStepSetup{
			middleware: &descriptorAppender{
				descriptor: descriptor,
			},
		},
	}
	var structured *structuredOutput
	if len(settings.OutputSchema) != 0 {
		structured = newStructuredOutput(settings.OutputSchema)
		setups = append(setups, structured)
	}
	return agent.ComposeSetups(
		childpolicy.DelegationSeed(owner.dependencies.Delegation),
		policies,
		agent.ComposeSetups(setups...),
		owner.dependencies.Extensions,
	), structured
}

type preStepSetup struct {
	middleware agent.PreStepMiddleware
}

func (contribution preStepSetup) Apply(
	_ context.Context,
	_ agent.Agent,
	editor agent.ScopeEditor,
) error {
	return editor.UsePreStep(contribution.middleware)
}

var _ agent.Setup = preStepSetup{}
