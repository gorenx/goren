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

// AgentProvider publishes one exact Agent and activates its execution
// capabilities only after the same-Scope overlays are available.
type AgentProvider struct {
	plugin.Base
	subject *ReactLoopAgent
}

func (provider *AgentProvider) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: PluginName + "/agent-runtime",
		Provides: []plugin.ProvidedService{
			plugin.NewProvidedService[agent.Agent](provider.subject),
		},
		Requires: []plugin.ServiceType{
			plugin.ServiceOf[session.LiveStore](),
			plugin.ServiceOf[llm.LlmRuntime](),
			plugin.ServiceOf[tools.ToolRuntime](),
			plugin.ServiceOf[systemprompt.Assembler](),
		},
	}
}

func (provider *AgentProvider) Apply(requestContext context.Context) error {
	sessions, err := plugin.Require[session.LiveStore](provider)
	if err != nil {
		return err
	}
	models, err := plugin.Require[llm.LlmRuntime](provider)
	if err != nil {
		return err
	}
	toolRuntime, err := plugin.Require[tools.ToolRuntime](provider)
	if err != nil {
		return err
	}
	prompts, err := plugin.Require[systemprompt.Assembler](provider)
	if err != nil {
		return err
	}
	return provider.subject.activate(
		requestContext,
		sessions,
		models,
		toolRuntime,
		prompts,
	)
}

// Agent returns the exact business capability owned by this Provider.
func (provider *AgentProvider) Agent() agent.Agent {
	if provider == nil {
		return nil
	}
	return provider.subject
}

func (provider *AgentProvider) Dispose(closeContext context.Context) error {
	if provider == nil || provider.subject == nil {
		return nil
	}
	return provider.subject.shutdown(closeContext)
}

var _ plugin.Plugin = (*AgentProvider)(nil)
