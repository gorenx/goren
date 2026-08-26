// Package controltool adapts Subagent control capabilities to model Tools.
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
	controls *controlTools
	handles  []*tools.ToolHandle
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
			plugin.ServiceOf[subagent.ChildControl](),
			plugin.ServiceOf[subagent.ChildDirectory](),
			plugin.ServiceOf[agent.Registry](),
			plugin.ServiceOf[tools.ToolCatalog](),
		},
	}
}

// Apply registers all control tools atomically.
func (owner *Plugin) Apply(requestContext context.Context) error {
	children, requireErr := plugin.Require[subagent.ChildControl](owner)
	if requireErr != nil {
		return requireErr
	}
	directory, requireErr := plugin.Require[subagent.ChildDirectory](owner)
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
	controls, constructionErr := newControlTools(children, directory, agents)
	if constructionErr != nil {
		return constructionErr
	}
	owner.controls = controls
	for _, definition := range controls.definitions() {
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
	owner.controls = nil
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
