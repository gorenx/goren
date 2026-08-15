package assembly

import (
	"context"
	"encoding/json"

	agentcore "github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/plugin"
)

// AgentConfig is intentionally empty: concrete Agent construction belongs to
// the Agent Loop plugin that registers the Registry's consumer-owned Factory.
type AgentConfig struct{}

type agentFactory struct{}

func (agentFactory) Name() string { return AgentFactoryName }

func (agentFactory) DecodeConfig(rawConfig json.RawMessage) (AgentConfig, error) {
	return plugin.DecodeStrictConfig[AgentConfig](rawConfig, nil)
}

func (agentFactory) New(context.Context, AgentConfig) (plugin.Plugin, error) {
	return &agentPlugin{}, nil
}

type agentPlugin struct{}

func (*agentPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{Name: AgentFactoryName, Provides: []plugin.ServiceRef{agentcore.Service.Ref()}}
}

func (*agentPlugin) Apply(_ context.Context, pluginScope *plugin.Scope) error {
	serviceValue, err := agentcore.NewRegistry(pluginScope, agentcore.RegistryOptions{})
	if err != nil {
		return err
	}
	_, err = plugin.Provide(pluginScope, agentcore.Service, serviceValue)
	return err
}
