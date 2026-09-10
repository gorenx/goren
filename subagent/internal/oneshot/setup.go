package oneshot

import (
	"context"

	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/subagent/internal/childpolicy"
)

func (owner *Service) configure(
	ctx context.Context,
	settings subagent.OneShotOptions,
) (subagent.ChildConfiguration, *structuredOutput, error) {
	configuration := subagent.ChildConfiguration{}
	childpolicy.Apply(childpolicy.PolicySet{
		Persona:         settings.Persona,
		ToolRestriction: settings.ToolFilter,
	}, &configuration)
	var structured *structuredOutput
	if len(settings.OutputSchema) != 0 {
		structured = newStructuredOutput(settings.OutputSchema)
		structured.Configure(&configuration)
	}
	if owner.dependencies.Extensions != nil {
		if err := owner.dependencies.Extensions.ConfigureChild(ctx, &configuration); err != nil {
			return subagent.ChildConfiguration{}, nil, err
		}
	}
	return configuration, structured, nil
}
