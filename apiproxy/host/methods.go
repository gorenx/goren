package host

import (
	"context"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentdefaultmodel"
	"github.com/gorenx/goren/apiproxy"
	sessionapi "github.com/gorenx/goren/apiproxy/session"
	"github.com/gorenx/goren/credentials"
	"github.com/gorenx/goren/llm"
	sessionquery "github.com/gorenx/goren/session/query"
)

func registerMethods(
	methods *apiproxy.Catalog,
	sessions *sessionapi.Gateway,
	queries sessionquery.QueryService,
	models llm.LlmRuntime,
	credentialProvider credentials.Provider,
	agents agent.Registry,
	defaults agentdefaultmodel.DefaultModel,
	deploymentSettings Settings,
	options RuntimeOptions,
) error {
	descriptionSource := apiproxy.HostDescriptionFunc(
		func(context.Context) (apiproxy.HostDescription, error) {
			selected := defaults.CurrentSelection()
			return apiproxy.HostDescription{
				Version:          deploymentSettings.Version,
				CWD:              options.WorkingDirectory,
				Provider:         selected.Provider,
				Model:            selected.Model,
				AttachedSessions: len(agents.List()),
				CanOpenPath:      false,
			}, nil
		},
	)
	if err := apiproxy.RegisterHostDescribe(methods, descriptionSource); err != nil {
		return err
	}
	presetGateway := apiproxy.NewAgentPresetGateway(
		nil,
		apiproxy.AgentPresetGatewayOptions{
			CanOpenPath: false,
		},
	)
	if err := apiproxy.RegisterAgentPresetListAPI(methods, presetGateway); err != nil {
		return err
	}
	settingsGateway := apiproxy.NewSettingsGateway(nil)
	if err := apiproxy.RegisterSettingsDescribeAPI(methods, settingsGateway); err != nil {
		return err
	}
	credentialsGateway := apiproxy.NewCredentialsGateway(credentialProvider)
	if err := apiproxy.RegisterCredentialsAPI(methods, credentialsGateway); err != nil {
		return err
	}
	llmGateway, err := apiproxy.NewLLMGateway(models)
	if err != nil {
		return err
	}
	if err = apiproxy.RegisterLLMAPI(methods, llmGateway); err != nil {
		return err
	}
	searchGateway, err := sessionapi.NewSearchGateway(queries, sessions)
	if err != nil {
		return err
	}
	if err = apiproxy.RegisterSessionAPI(methods, sessions, searchGateway); err != nil {
		return err
	}
	return nil
}
