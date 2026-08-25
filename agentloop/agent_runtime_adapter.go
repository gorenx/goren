package agentloop

import (
	"context"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/systemprompt"
	"github.com/gorenx/goren/tools"
)

// agentRuntimeAdapter publishes one exact Agent and activates its business
// loop only after the same-Scope overlays are available.
type agentRuntimeAdapter struct {
	plugin.Base
	subject *ReactLoopAgent
}

func (adapter *agentRuntimeAdapter) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: PluginName + "/agent-runtime",
		Provides: []plugin.ProvidedService{
			plugin.NewProvidedService[agent.Agent](adapter.subject),
		},
		Requires: []plugin.ServiceType{
			plugin.ServiceOf[session.LiveStore](),
			plugin.ServiceOf[llm.LlmRuntime](),
			plugin.ServiceOf[tools.ToolRuntime](),
			plugin.ServiceOf[systemprompt.Assembler](),
		},
	}
}

func (adapter *agentRuntimeAdapter) Apply(requestContext context.Context) error {
	sessions, err := plugin.Require[session.LiveStore](adapter)
	if err != nil {
		return err
	}
	models, err := plugin.Require[llm.LlmRuntime](adapter)
	if err != nil {
		return err
	}
	toolRuntime, err := plugin.Require[tools.ToolRuntime](adapter)
	if err != nil {
		return err
	}
	prompts, err := plugin.Require[systemprompt.Assembler](adapter)
	if err != nil {
		return err
	}
	return adapter.subject.activate(
		requestContext,
		sessions,
		models,
		toolRuntime,
		prompts,
	)
}

func (*agentRuntimeAdapter) Dispose(context.Context) error { return nil }

var _ plugin.Plugin = (*agentRuntimeAdapter)(nil)
