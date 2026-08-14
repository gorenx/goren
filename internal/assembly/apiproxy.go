package assembly

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/gorenx/goren/apiproxy"
	protocol "github.com/gorenx/goren/connection"
	connectionhost "github.com/gorenx/goren/internal/connection"
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
	return &apiProxyPlugin{settings: settings, workingDirectory: builder.workingDirectory}, nil
}

type apiProxyPlugin struct {
	settings         APIProxyConfig
	workingDirectory string
}

func (instance *apiProxyPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: APIProxyFactoryName, Provides: []plugin.ServiceRef{apiProxyServiceKey.Ref()},
		Requires: []plugin.ServiceRef{session.StoreService.Ref()},
	}
}

func (instance *apiProxyPlugin) Apply(_ context.Context, pluginScope *plugin.Scope) error {
	sessionStore, found := plugin.Require(pluginScope, session.StoreService)
	if !found {
		return errors.New("assembly: sessions dependency is unavailable")
	}
	methods := apiproxy.NewCatalog()
	descriptionSource := apiproxy.HostDescriptionFunc(func(context.Context) (apiproxy.HostDescription, error) {
		return apiproxy.HostDescription{
			Version: instance.settings.Version, CWD: instance.workingDirectory,
			AttachedSessions: len(sessionStore.List()), CanOpenPath: false,
		}, nil
	})
	if err := apiproxy.RegisterHostDescribe(methods, descriptionSource); err != nil {
		return err
	}
	idleMuxStream := func(requestContext context.Context, _ func(apiproxy.StreamRequest[apiproxy.MuxFrame]) error) error {
		<-requestContext.Done()
		return nil
	}
	idleHostStream := func(requestContext context.Context, _ func(apiproxy.StreamRequest[apiproxy.HostFrame]) error) error {
		<-requestContext.Done()
		return nil
	}
	streams, err := apiproxy.NewEventStreams(idleMuxStream, idleHostStream)
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
