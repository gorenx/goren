// Package control implements model-facing continuable Subagent control tools.
package control

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/tools"
)

const PluginName = "@deepseek-ai/dsh-tool-subagent-control"

// Plugin owns send_message, interrupt_agent, and list_agents registrations.
type Plugin struct {
	plugin.Base
	continuations subagent.ContinuableService
	catalog       subagent.Catalog
	agents        agent.Registry
	tools         tools.ToolCatalog
	handles       []*tools.ToolHandle
}

// New constructs an inactive control Plugin.
func New() *Plugin {
	return &Plugin{}
}

// Manifest declares the control use-case boundaries.
func (*Plugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: PluginName,
		Requires: []plugin.ServiceType{
			plugin.ServiceOf[subagent.ContinuableService](),
			plugin.ServiceOf[subagent.Catalog](),
			plugin.ServiceOf[agent.Registry](),
			plugin.ServiceOf[tools.ToolCatalog](),
		},
	}
}

// Apply registers all control tools atomically.
func (owner *Plugin) Apply(requestContext context.Context) error {
	continuations, requireErr := plugin.Require[subagent.ContinuableService](owner)
	if requireErr != nil {
		return requireErr
	}
	catalog, requireErr := plugin.Require[subagent.Catalog](owner)
	if requireErr != nil {
		return requireErr
	}
	agents, requireErr := plugin.Require[agent.Registry](owner)
	if requireErr != nil {
		return requireErr
	}
	toolCatalog, requireErr := plugin.Require[tools.ToolCatalog](owner)
	if requireErr != nil {
		return requireErr
	}
	owner.continuations = continuations
	owner.catalog = catalog
	owner.agents = agents
	owner.tools = toolCatalog
	definitions := []tools.ToolDefinition{
		owner.sendMessageDefinition(),
		owner.interruptDefinition(),
		owner.listDefinition(),
	}
	for _, definition := range definitions {
		handle, addErr := toolCatalog.AddTool(requestContext, definition)
		if addErr != nil {
			rollbackErr := owner.release(context.WithoutCancel(requestContext))
			return errors.Join(addErr, rollbackErr)
		}
		owner.handles = append(owner.handles, handle)
	}
	return nil
}

// Dispose removes all control tools in reverse registration order.
func (owner *Plugin) Dispose(closeContext context.Context) error {
	if closeContext == nil {
		closeContext = context.Background()
	}
	releaseErr := owner.release(context.WithoutCancel(closeContext))
	owner.continuations = nil
	owner.catalog = nil
	owner.agents = nil
	owner.tools = nil
	return releaseErr
}

func (owner *Plugin) release(closeContext context.Context) error {
	var releaseErr error
	for index := len(owner.handles) - 1; index >= 0; index-- {
		releaseErr = errors.Join(
			releaseErr,
			owner.handles[index].Unregister(closeContext),
		)
	}
	owner.handles = nil
	return releaseErr
}
