package assembly

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"time"

	connectionhost "github.com/gorenx/goren/internal/connection"
	"github.com/gorenx/goren/plugin"
)

type serverService interface {
	Address() string
}

var serverServiceKey = plugin.DefineService[serverService]("webServer")

// ConnectionConfig contains the Connection Host deployment settings.
type ConnectionConfig struct {
	ListenAddress         string   `json:"listenAddress"`
	TrustedHosts          []string `json:"trustedHosts,omitempty"`
	MaxBodyBytes          int64    `json:"maxBodyBytes,omitempty"`
	GracefulTimeoutMillis int64    `json:"gracefulTimeoutMillis,omitempty"`
	ServeWeb              bool     `json:"serveWeb,omitempty"`
}

type connectionFactory struct{}

func (connectionFactory) Name() string {
	return ConnectionFactoryName
}

func (connectionFactory) DecodeConfig(rawConfig json.RawMessage) (ConnectionConfig, error) {
	return plugin.DecodeStrictConfig(rawConfig, func(settings ConnectionConfig) error {
		if strings.TrimSpace(settings.ListenAddress) == "" || settings.ListenAddress != strings.TrimSpace(settings.ListenAddress) {
			return errors.New("listenAddress must be non-empty and trimmed")
		}
		if settings.MaxBodyBytes < 0 {
			return errors.New("maxBodyBytes must not be negative")
		}
		if settings.GracefulTimeoutMillis < 0 {
			return errors.New("gracefulTimeoutMillis must not be negative")
		}
		return nil
	})
}

func (connectionFactory) New(_ context.Context, settings ConnectionConfig) (plugin.Plugin, error) {
	return &connectionPlugin{settings: settings}, nil
}

type connectionPlugin struct {
	settings ConnectionConfig
}

func (instance *connectionPlugin) Manifest() plugin.Manifest {
	pluginDescriptor := plugin.Manifest{
		Name:     ConnectionFactoryName,
		Provides: []plugin.ServiceRef{serverServiceKey.Ref()},
		Requires: []plugin.ServiceRef{apiProxyServiceKey.Ref()},
	}
	if instance.settings.ServeWeb {
		pluginDescriptor.Requires = append(pluginDescriptor.Requires, webFrontendServiceKey.Ref())
	}
	return pluginDescriptor
}

func (instance *connectionPlugin) Apply(requestContext context.Context, pluginScope *plugin.Scope) error {
	proxy, found := plugin.Require(pluginScope, apiProxyServiceKey)
	if !found {
		return errors.New("assembly: apiProxy dependency is unavailable")
	}
	var frontend webFrontend
	if instance.settings.ServeWeb {
		var frontendFound bool
		frontend, frontendFound = plugin.Require(pluginScope, webFrontendServiceKey)
		if !frontendFound {
			return errors.New("assembly: web frontend dependency is unavailable")
		}
	}
	gracefulTimeout := time.Duration(instance.settings.GracefulTimeoutMillis) * time.Millisecond
	carrier, err := connectionhost.NewHTTPHost(connectionhost.HTTPConfig{
		TrustedHosts: instance.settings.TrustedHosts, MaxBodyBytes: instance.settings.MaxBodyBytes,
		GracefulTimeout: gracefulTimeout, Frontend: frontend,
	}, proxy, proxy)
	if err != nil {
		return err
	}
	var endpoint *boundServer
	if err := pluginScope.Effect(requestContext, "connection-host", func(lifecycle context.Context) (plugin.Disposer, error) {
		listener, listenErr := net.Listen("tcp", instance.settings.ListenAddress)
		if listenErr != nil {
			return nil, listenErr
		}
		serveContext, cancelServe := context.WithCancel(lifecycle)
		finished := make(chan error, 1)
		go func() {
			finished <- carrier.Serve(serveContext, listener)
		}()
		endpoint = &boundServer{listenAddress: listener.Addr().String()}
		return func(closeContext context.Context) error {
			cancelServe()
			select {
			case serveErr := <-finished:
				return serveErr
			case <-closeContext.Done():
				return errors.Join(closeContext.Err(), listener.Close(), carrier.Close(closeContext))
			}
		}, nil
	}); err != nil {
		return err
	}
	_, err = plugin.Provide(pluginScope, serverServiceKey, serverService(endpoint))
	return err
}

type boundServer struct {
	listenAddress string
}

func (endpoint *boundServer) Address() string {
	return endpoint.listenAddress
}
