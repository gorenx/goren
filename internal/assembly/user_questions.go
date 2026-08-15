package assembly

import (
	"context"
	"encoding/json"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/userquestions"
)

type UserQuestionsConfig struct{}

type userQuestionsFactory struct{}

func (userQuestionsFactory) Name() string { return UserQuestionsFactoryName }

func (userQuestionsFactory) DecodeConfig(rawConfig json.RawMessage) (UserQuestionsConfig, error) {
	return plugin.DecodeStrictConfig[UserQuestionsConfig](rawConfig, nil)
}

func (userQuestionsFactory) New(context.Context, UserQuestionsConfig) (plugin.Plugin, error) {
	return &userQuestionsPlugin{}, nil
}

type userQuestionsPlugin struct{}

func (*userQuestionsPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: UserQuestionsFactoryName, Provides: []plugin.ServiceRef{userquestions.Service.Ref()},
		Optional: []plugin.ServiceRef{agent.Service.Ref()},
	}
}

func (*userQuestionsPlugin) Apply(_ context.Context, pluginScope *plugin.Scope) error {
	agentResolver := userquestions.AgentRegistryResolverFunc(func() (agent.Registry, bool) {
		return plugin.Require(pluginScope, agent.Service)
	})
	questionService := userquestions.New(agentResolver)
	_, err := plugin.Provide(pluginScope, userquestions.Service, questionService)
	return err
}
