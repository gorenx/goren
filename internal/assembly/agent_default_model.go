package assembly

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentdefaultmodel"
	"github.com/gorenx/goren/plugin"
)

// AgentDefaultModelConfig is the composition-backed default for future Agents.
type AgentDefaultModelConfig struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

type agentDefaultModelFactory struct{}

func (agentDefaultModelFactory) Name() string { return AgentDefaultModelFactoryName }

func (agentDefaultModelFactory) DecodeConfig(rawConfig json.RawMessage) (AgentDefaultModelConfig, error) {
	return plugin.DecodeStrictConfig(rawConfig, func(settings AgentDefaultModelConfig) error {
		if strings.TrimSpace(settings.Provider) == "" || strings.TrimSpace(settings.Model) == "" {
			return errors.New("provider and model must be non-empty")
		}
		return nil
	})
}

func (agentDefaultModelFactory) New(_ context.Context, settings AgentDefaultModelConfig) (plugin.Plugin, error) {
	return &agentDefaultModelPlugin{settings: settings}, nil
}

type agentDefaultModelPlugin struct {
	settings AgentDefaultModelConfig
}

func (*agentDefaultModelPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: AgentDefaultModelFactoryName, Provides: []plugin.ServiceRef{agentdefaultmodel.Service.Ref()},
	}
}

func (instance *agentDefaultModelPlugin) Apply(_ context.Context, pluginScope *plugin.Scope) error {
	provider, err := agentdefaultmodel.NewStatic(agent.ModelSelection{
		Provider: instance.settings.Provider, Model: instance.settings.Model,
	})
	if err != nil {
		return err
	}
	_, err = plugin.Provide(pluginScope, agentdefaultmodel.Service, provider)
	return err
}
