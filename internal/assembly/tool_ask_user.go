package assembly

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/toolaskuser"
	"github.com/gorenx/goren/tools"
	"github.com/gorenx/goren/userquestions"
)

type ToolAskUserConfig struct{}

type toolAskUserFactory struct{}

func (toolAskUserFactory) Name() string { return ToolAskUserFactoryName }

func (toolAskUserFactory) DecodeConfig(rawConfig json.RawMessage) (ToolAskUserConfig, error) {
	return plugin.DecodeStrictConfig[ToolAskUserConfig](rawConfig, nil)
}

func (toolAskUserFactory) New(context.Context, ToolAskUserConfig) (plugin.Plugin, error) {
	return &toolAskUserPlugin{}, nil
}

type toolAskUserPlugin struct{}

func (*toolAskUserPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name:     ToolAskUserFactoryName,
		Requires: []plugin.ServiceRef{tools.Service.Ref(), userquestions.Service.Ref()},
	}
}

func (*toolAskUserPlugin) Apply(requestContext context.Context, pluginScope *plugin.Scope) error {
	toolService, toolsFound := plugin.Require(pluginScope, tools.Service)
	questionService, questionsFound := plugin.Require(pluginScope, userquestions.Service)
	if !toolsFound || !questionsFound {
		return errors.New("assembly: ask_user_question dependencies are unavailable")
	}
	definition, err := toolaskuser.New(questionService)
	if err != nil {
		return err
	}
	_, err = toolService.Register(requestContext, pluginScope, definition)
	return err
}
