package assembly

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	agentcore "github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentdefaultmodel"
	"github.com/gorenx/goren/apiproxy"
	protocol "github.com/gorenx/goren/connection"
	connectionhost "github.com/gorenx/goren/internal/connection"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
)

type apiProxyService interface {
	connectionhost.RPCDispatcher
	connectionhost.EventSource
}

var apiProxyServiceKey = plugin.DefineService[apiProxyService]("apiProxy")

// APIProxyConfig configures the currently included Host API surface.
type APIProxyConfig struct {
	Version string `json:"version"`
}

type apiProxyFactory struct {
	workingDirectory string
	ensureDirectory  func(string) error
}

func (builder apiProxyFactory) Name() string {
	return APIProxyFactoryName
}

func (builder apiProxyFactory) DecodeConfig(rawConfig json.RawMessage) (APIProxyConfig, error) {
	return plugin.DecodeStrictConfig(rawConfig, func(settings APIProxyConfig) error {
		if strings.TrimSpace(settings.Version) == "" {
			return errors.New("version must be non-empty")
		}
		return nil
	})
}

func (builder apiProxyFactory) New(_ context.Context, settings APIProxyConfig) (plugin.Plugin, error) {
	return &apiProxyPlugin{
		settings: settings, workingDirectory: builder.workingDirectory,
		ensureDirectory: builder.ensureDirectory,
	}, nil
}

type apiProxyPlugin struct {
	settings         APIProxyConfig
	workingDirectory string
	ensureDirectory  func(string) error
}

func (instance *apiProxyPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: APIProxyFactoryName, Provides: []plugin.ServiceRef{apiProxyServiceKey.Ref()},
		Requires: []plugin.ServiceRef{
			agentcore.Service.Ref(), agentdefaultmodel.Service.Ref(), llm.Service.Ref(), session.StoreService.Ref(),
		},
	}
}

func (instance *apiProxyPlugin) Apply(requestContext context.Context, pluginScope *plugin.Scope) error {
	agentRegistry, agentsFound := plugin.Require(pluginScope, agentcore.Service)
	defaultModels, defaultsFound := plugin.Require(pluginScope, agentdefaultmodel.Service)
	modelRuntime, modelsFound := plugin.Require(pluginScope, llm.Service)
	sessionStore, found := plugin.Require(pluginScope, session.StoreService)
	if !agentsFound || !defaultsFound || !modelsFound || !found {
		return errors.New("assembly: API Proxy dependencies are unavailable")
	}
	gateway, err := apiproxy.NewSessionGateway(requestContext, pluginScope, apiproxy.SessionGatewayDependencies{
		Agents: agentRegistry, Sessions: sessionStore, LLM: modelRuntime, Defaults: defaultModels,
		Directories: apiproxy.DirectoryProvisionerFunc(instance.ensureDirectory),
	}, apiproxy.SessionGatewayOptions{WorkingDirectory: instance.workingDirectory})
	if err != nil {
		return err
	}
	methods := apiproxy.NewCatalog()
	descriptionSource := apiproxy.HostDescriptionFunc(func(context.Context) (apiproxy.HostDescription, error) {
		selected := defaultModels.CurrentSelection()
		return apiproxy.HostDescription{
			Version: instance.settings.Version, CWD: instance.workingDirectory,
			Provider: selected.Provider, Model: selected.Model,
			AttachedSessions: len(agentRegistry.List()), CanOpenPath: false,
		}, nil
	})
	if err := apiproxy.RegisterHostDescribe(methods, descriptionSource); err != nil {
		return err
	}
	if err := apiproxy.RegisterSessionAPI(methods, gateway); err != nil {
		return err
	}
	streams, err := apiproxy.NewEventStreams(gateway.Mux, gateway.Host)
	if err != nil {
		return err
	}
	binding := &apiProxyBinding{methods: methods, streams: streams}
	_, err = plugin.Provide(pluginScope, apiProxyServiceKey, apiProxyService(binding))
	return err
}

type apiProxyBinding struct {
	methods *apiproxy.Catalog
	streams *apiproxy.EventStreams
}

func (binding *apiProxyBinding) HasUnary(method string) bool {
	return binding.methods.HasUnary(method)
}

func (binding *apiProxyBinding) DispatchUnary(requestContext context.Context, method string, rpcID protocol.RPCID, payload json.RawMessage) (protocol.RPCResult, error) {
	return binding.methods.DispatchUnary(requestContext, method, rpcID, payload)
}

func (binding *apiProxyBinding) Respond(requestContext context.Context, response protocol.ClientResponse) (protocol.RPCReceipt, error) {
	return binding.methods.Respond(requestContext, response)
}

func (binding *apiProxyBinding) Mux(requestContext context.Context, emit func(protocol.RPCRequest) error) error {
	return binding.streams.Mux(requestContext, emit)
}

func (binding *apiProxyBinding) Host(requestContext context.Context, emit func(protocol.RPCRequest) error) error {
	return binding.streams.Host(requestContext, emit)
}
