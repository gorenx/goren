package report

import (
	"context"
	"errors"

	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/systemprompt"
	"github.com/gorenx/goren/tools"
)

const reportPrompt = "Deliver your result with the report tool before you finish: call it once with a self-contained answer. The agent that started you does not automatically receive your transcript, tool output, or reasoning. Report earlier as well whenever a partial finding changes what that agent should do next; reporting never ends your turn."

type childPlugin struct {
	plugin.Base
	installation *installation
	tool         *reportTool
	toolHandle   *tools.ToolHandle
	promptHandle *systemprompt.PromptHandle
}

func (*childPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: PluginName + "/child",
		Requires: []plugin.ServiceType{
			plugin.ServiceOf[reportToolProvider](),
			plugin.ServiceOf[tools.ToolCatalog](),
			plugin.ServiceOf[systemprompt.PromptRegistry](),
		},
	}
}

func (child *childPlugin) Apply(requestContext context.Context) error {
	reportOwner, requireErr := plugin.Require[reportToolProvider](child)
	if requireErr != nil {
		return requireErr
	}
	reporting := reportOwner.Tool()
	if reporting == nil {
		return errors.New("subagent report: delivery Service is unavailable")
	}
	child.tool = reporting
	toolCatalog, requireErr := plugin.Require[tools.ToolCatalog](child)
	if requireErr != nil {
		return requireErr
	}
	prompts, requireErr := plugin.Require[systemprompt.PromptRegistry](child)
	if requireErr != nil {
		return requireErr
	}
	promptHandle, addErr := prompts.AddSection(
		requestContext,
		systemprompt.PromptSection{
			Name:  "tool:report",
			Order: 117,
			Text:  systemprompt.StaticText(reportPrompt),
		},
	)
	if addErr != nil {
		return addErr
	}
	child.promptHandle = promptHandle
	toolHandle, addErr := toolCatalog.AddTool(
		requestContext,
		child.tool.definition(),
	)
	if addErr != nil {
		return errors.Join(
			addErr,
			child.release(context.WithoutCancel(requestContext)),
		)
	}
	child.toolHandle = toolHandle
	return nil
}

func (child *childPlugin) Dispose(closeContext context.Context) error {
	if closeContext == nil {
		closeContext = context.Background()
	}
	releaseErr := child.release(context.WithoutCancel(closeContext))
	child.tool = nil
	if child.installation != nil {
		child.installation.release(releaseErr)
	}
	return releaseErr
}

func (child *childPlugin) release(closeContext context.Context) error {
	var releaseErr error
	if child.toolHandle != nil {
		releaseErr = errors.Join(
			releaseErr,
			child.toolHandle.Unregister(closeContext),
		)
		child.toolHandle = nil
	}
	if child.promptHandle != nil {
		releaseErr = errors.Join(
			releaseErr,
			child.promptHandle.Unregister(closeContext),
		)
		child.promptHandle = nil
	}
	return releaseErr
}

var _ plugin.Plugin = (*childPlugin)(nil)
