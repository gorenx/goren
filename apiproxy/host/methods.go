package host

import (
	"context"
	"sort"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentdefaultmodel"
	"github.com/gorenx/goren/apiproxy"
	sessionapi "github.com/gorenx/goren/apiproxy/session"
	"github.com/gorenx/goren/commands"
	"github.com/gorenx/goren/credentials"
	"github.com/gorenx/goren/llm"
	sessionquery "github.com/gorenx/goren/session/query"
	"github.com/gorenx/goren/subagent"
	boundcontract "github.com/gorenx/goren/subagent/bound"
	"github.com/gorenx/goren/tools"
)

func registerMethods(
	methods *apiproxy.Catalog,
	sessions *sessionapi.Gateway,
	queries sessionquery.QueryService,
	commandRegistry commands.Registry,
	models llm.LlmRuntime,
	credentialProvider credentials.Provider,
	boundDefinitions boundcontract.Definitions,
	extensionDirectory subagent.ExtensionDirectory,
	rootTools tools.ToolRuntime,
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
	boundGateway := apiproxy.NewBoundGateway(
		apiproxy.BoundGatewayDependencies{
			Definitions: boundDefinitions,
			Tools: apiproxy.BoundToolDirectoryFunc(func(
				requestContext context.Context,
			) ([]apiproxy.BoundToolOption, error) {
				if err := requestContext.Err(); err != nil {
					return nil, err
				}
				schemas := rootTools.Schemas()
				toolOptions := make(
					[]apiproxy.BoundToolOption,
					len(schemas),
				)
				for schemaIndex, schema := range schemas {
					toolOptions[schemaIndex] = apiproxy.BoundToolOption{
						Name:        schema.Name,
						Description: schema.Description,
					}
				}
				sort.Slice(toolOptions, func(leftIndex int, rightIndex int) bool {
					return toolOptions[leftIndex].Name < toolOptions[rightIndex].Name
				})
				return toolOptions, nil
			}),
			Extensions: apiproxy.BoundExtensionDirectoryFunc(func(
				requestContext context.Context,
			) ([]apiproxy.BoundExtensionOption, error) {
				if err := requestContext.Err(); err != nil {
					return nil, err
				}
				descriptors := extensionDirectory.ListExtensions()
				extensionOptions := make(
					[]apiproxy.BoundExtensionOption,
					len(descriptors),
				)
				for descriptorIndex, descriptor := range descriptors {
					extensionOptions[descriptorIndex] = apiproxy.BoundExtensionOption{
						Name: descriptor.Name,
					}
				}
				sort.Slice(extensionOptions, func(
					leftIndex int,
					rightIndex int,
				) bool {
					return extensionOptions[leftIndex].Name < extensionOptions[rightIndex].Name
				})
				return extensionOptions, nil
			}),
		},
	)
	if err := apiproxy.RegisterBoundAPI(methods, boundGateway); err != nil {
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
	commandAdapter, err := apiproxy.NewCommandsGateway(
		commandRegistry,
		sessions,
	)
	if err != nil {
		return err
	}
	if err = apiproxy.RegisterCommandsAPI(methods, commandAdapter); err != nil {
		return err
	}
	return nil
}
