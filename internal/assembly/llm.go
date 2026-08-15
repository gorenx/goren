package assembly

import (
	"context"
	"encoding/json"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
)

// LLMConfig is intentionally empty: provider routes and credentials belong to adapter plugins.
type LLMConfig struct{}

type llmFactory struct{}

func (llmFactory) Name() string { return LLMFactoryName }

func (llmFactory) DecodeConfig(rawConfig json.RawMessage) (LLMConfig, error) {
	return plugin.DecodeStrictConfig[LLMConfig](rawConfig, nil)
}

func (llmFactory) New(context.Context, LLMConfig) (plugin.Plugin, error) {
	return &llmPlugin{}, nil
}

type llmPlugin struct{}

func (*llmPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{Name: LLMFactoryName, Provides: []plugin.ServiceRef{llm.Service.Ref()}}
}

func (*llmPlugin) Apply(_ context.Context, pluginScope *plugin.Scope) error {
	serviceValue, err := llm.NewRuntime(pluginScope, nil)
	if err != nil {
		return err
	}
	_, err = plugin.Provide(pluginScope, llm.Service, serviceValue)
	return err
}
