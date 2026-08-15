package assembly

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/gorenx/goren/approval"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/systemprompt"
	toolscore "github.com/gorenx/goren/tools"
)

type toolsFactory struct{}

func (toolsFactory) Name() string { return ToolsFactoryName }

func (toolsFactory) DecodeConfig(rawConfig json.RawMessage) (toolscore.ValidatedConfig, error) {
	settings, err := plugin.DecodeStrictConfig[toolscore.Config](rawConfig, nil)
	if err != nil {
		return toolscore.ValidatedConfig{}, err
	}
	return toolscore.ValidateConfig(settings)
}

func (toolsFactory) New(_ context.Context, settings toolscore.ValidatedConfig) (plugin.Plugin, error) {
	return &toolsPlugin{settings: settings}, nil
}

type toolsPlugin struct {
	settings toolscore.ValidatedConfig
}

func (instance *toolsPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: ToolsFactoryName, Provides: []plugin.ServiceRef{toolscore.Service.Ref()},
		Requires: []plugin.ServiceRef{systemprompt.Service.Ref()},
		Optional: []plugin.ServiceRef{approval.Service.Ref()},
	}
}

func (instance *toolsPlugin) Apply(requestContext context.Context, pluginScope *plugin.Scope) error {
	promptService, found := plugin.Require(pluginScope, systemprompt.Service)
	if !found {
		return errors.New("assembly: required systemPrompt service is unavailable")
	}
	approvalResolver := toolscore.ApprovalResolverFunc(func() (approval.Approval, bool) {
		return plugin.Require(pluginScope, approval.Service)
	})
	toolService, err := toolscore.New(
		requestContext, pluginScope, promptService, approvalResolver, nil, instance.settings,
	)
	if err != nil {
		return err
	}
	_, err = plugin.Provide(pluginScope, toolscore.Service, toolService)
	return err
}
