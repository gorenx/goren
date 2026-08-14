package assembly

import (
	"context"
	"encoding/json"

	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/systemprompt"
)

type systemPromptFactory struct{}

func (systemPromptFactory) Name() string {
	return SystemPromptFactoryName
}

func (systemPromptFactory) DecodeConfig(rawConfig json.RawMessage) (systemprompt.ValidatedConfig, error) {
	settings, err := plugin.DecodeStrictConfig[systemprompt.Config](rawConfig, nil)
	if err != nil {
		return systemprompt.ValidatedConfig{}, err
	}
	return systemprompt.ValidateConfig(settings)
}

func (systemPromptFactory) New(_ context.Context, settings systemprompt.ValidatedConfig) (plugin.Plugin, error) {
	return &systemPromptPlugin{settings: settings}, nil
}

type systemPromptPlugin struct {
	settings systemprompt.ValidatedConfig
}

func (instance *systemPromptPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{Name: SystemPromptFactoryName, Provides: []plugin.ServiceRef{systemprompt.Service.Ref()}}
}

func (instance *systemPromptPlugin) Apply(requestContext context.Context, pluginScope *plugin.Scope) error {
	promptService, err := systemprompt.New(requestContext, pluginScope, instance.settings)
	if err != nil {
		return err
	}
	_, err = plugin.Provide(pluginScope, systemprompt.Service, promptService)
	return err
}
