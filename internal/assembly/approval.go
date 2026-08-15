package assembly

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/gorenx/goren/approval"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/systemprompt"
)

type approvalFactory struct{}

func (approvalFactory) Name() string { return ApprovalFactoryName }

func (approvalFactory) DecodeConfig(rawConfig json.RawMessage) (approval.ValidatedConfig, error) {
	settings, err := plugin.DecodeStrictConfig[approval.Config](rawConfig, nil)
	if err != nil {
		return approval.ValidatedConfig{}, err
	}
	return approval.ValidateConfig(settings)
}

func (approvalFactory) New(_ context.Context, settings approval.ValidatedConfig) (plugin.Plugin, error) {
	return &approvalPlugin{settings: settings}, nil
}

type approvalPlugin struct {
	settings approval.ValidatedConfig
}

func (*approvalPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: ApprovalFactoryName, Provides: []plugin.ServiceRef{approval.Service.Ref()},
		Requires: []plugin.ServiceRef{systemprompt.Service.Ref()},
	}
}

func (instance *approvalPlugin) Apply(requestContext context.Context, pluginScope *plugin.Scope) error {
	promptService, found := plugin.Require(pluginScope, systemprompt.Service)
	if !found {
		return errors.New("assembly: required systemPrompt service is unavailable")
	}
	approvalService, err := approval.New(
		requestContext, pluginScope, promptService, instance.settings, approval.RuntimeOptions{},
	)
	if err != nil {
		return err
	}
	_, err = plugin.Provide(pluginScope, approval.Service, approvalService)
	return err
}
