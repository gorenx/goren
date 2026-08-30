package tools

import (
	"context"

	"github.com/gorenx/goren/plugin"
)

// ResolvePreExecute adapts a Tool ToolLayer request to the global Waterfall.
func (owner *Service) ResolvePreExecute(
	requestContext context.Context,
	request PreExecuteRequest,
) (PreExecuteOutcome, error) {
	return plugin.Run(requestContext, owner, request, preExecuteTerminal{})
}

// ResolveExecute adapts a Tool ToolLayer request to the global Waterfall.
func (owner *Service) ResolveExecute(
	requestContext context.Context,
	request ExecuteRequest,
	terminal ExecuteAction,
) (ExecuteOutcome, error) {
	return plugin.Run(requestContext, owner, request, terminal)
}

// ResolvePostExecute adapts a Tool ToolLayer request to the global Waterfall.
func (owner *Service) ResolvePostExecute(
	requestContext context.Context,
	request PostExecuteRequest,
) (PostExecuteOutcome, error) {
	return plugin.Run(requestContext, owner, request, postExecuteTerminal{})
}

// PublishCompleted adapts a Tool result to the global Plugin Event.
func (owner *Service) PublishCompleted(
	requestContext context.Context,
	completed ExecutionCompleted,
) error {
	return plugin.Publish(requestContext, owner, completed)
}

// PublishChanged adapts a Tool mutation to the global Plugin Event.
func (owner *Service) PublishChanged(requestContext context.Context) error {
	return plugin.Publish(requestContext, owner, RegistryChanged{})
}
