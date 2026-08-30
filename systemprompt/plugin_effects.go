package systemprompt

import (
	"context"

	"github.com/gorenx/goren/plugin"
)

// ResolveAssembly adapts PromptLayer assembly to the global Plugin Waterfall.
func (owner *Registry) ResolveAssembly(
	requestContext context.Context,
	request AssembleRequest,
	candidate PromptAssembly,
) (PromptAssembly, error) {
	return plugin.Run(
		requestContext,
		owner,
		request,
		&assemblyAction{
			candidate: candidate,
		},
	)
}

// PublishChanged adapts PromptLayer mutation to the global Plugin Event.
func (owner *Registry) PublishChanged(requestContext context.Context) error {
	return plugin.Publish(requestContext, owner, Changed{})
}
