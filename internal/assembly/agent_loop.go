package assembly

import (
	"context"
	"encoding/json"
	"errors"

	agentcore "github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentloop"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/sessionpersistence"
	"github.com/gorenx/goren/systemprompt"
	toolscore "github.com/gorenx/goren/tools"
)

type agentLoopFactory struct{}

func (agentLoopFactory) Name() string { return AgentLoopFactoryName }

func (agentLoopFactory) DecodeConfig(rawConfig json.RawMessage) (agentloop.ValidatedConfig, error) {
	settings, err := plugin.DecodeStrictConfig[agentloop.Config](rawConfig, nil)
	if err != nil {
		return agentloop.ValidatedConfig{}, err
	}
	return agentloop.ValidateConfig(settings)
}

func (agentLoopFactory) New(_ context.Context, settings agentloop.ValidatedConfig) (plugin.Plugin, error) {
	return &agentLoopPlugin{settings: settings}, nil
}

type agentLoopPlugin struct {
	settings agentloop.ValidatedConfig
}

func (*agentLoopPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: AgentLoopFactoryName, Provides: []plugin.ServiceRef{agentloop.Service.Ref()},
		Requires: []plugin.ServiceRef{
			agentcore.Service.Ref(), session.StoreService.Ref(), llm.Service.Ref(),
			toolscore.Service.Ref(), systemprompt.Service.Ref(),
		},
		Optional: []plugin.ServiceRef{sessionpersistence.Service.Ref()},
	}
}

func (instance *agentLoopPlugin) Apply(requestContext context.Context, pluginScope *plugin.Scope) error {
	agentRegistry, agentsFound := plugin.Require(pluginScope, agentcore.Service)
	sessionStore, sessionsFound := plugin.Require(pluginScope, session.StoreService)
	modelRuntime, modelsFound := plugin.Require(pluginScope, llm.Service)
	toolRuntime, toolsFound := plugin.Require(pluginScope, toolscore.Service)
	promptRuntime, promptsFound := plugin.Require(pluginScope, systemprompt.Service)
	if !agentsFound || !sessionsFound || !modelsFound || !toolsFound || !promptsFound {
		return errors.New("assembly: Agent Loop dependencies are unavailable")
	}
	loopRuntime, err := agentloop.New(requestContext, pluginScope, agentloop.Dependencies{
		Agents: agentRegistry, Sessions: sessionStore, LLM: modelRuntime,
		Tools: toolRuntime, SystemPrompt: promptRuntime,
	}, instance.settings, agentloop.RuntimeOptions{})
	if err != nil {
		return err
	}
	_, err = plugin.Provide(pluginScope, agentloop.Service, loopRuntime)
	return err
}
