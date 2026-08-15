package assembly

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llmretry"
	"github.com/gorenx/goren/plugin"
)

type llmRetryFactory struct{}

func (llmRetryFactory) Name() string { return LLMRetryFactoryName }

func (llmRetryFactory) DecodeConfig(rawConfig json.RawMessage) (llmretry.Config, error) {
	return plugin.DecodeStrictConfig[llmretry.Config](rawConfig, nil)
}

func (llmRetryFactory) New(_ context.Context, _ llmretry.Config) (plugin.Plugin, error) {
	return &llmRetryPlugin{}, nil
}

type llmRetryPlugin struct{}

func (*llmRetryPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{Name: LLMRetryFactoryName, Requires: []plugin.ServiceRef{agent.Service.Ref()}}
}

func (*llmRetryPlugin) Apply(requestContext context.Context, pluginScope *plugin.Scope) error {
	if _, found := plugin.Require(pluginScope, agent.Service); !found {
		return errors.New("assembly: required agents service is unavailable")
	}
	_, err := llmretry.Install(requestContext, pluginScope, llmretry.RuntimeOptions{})
	return err
}
