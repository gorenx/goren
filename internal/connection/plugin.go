package connection

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/gorenx/goren/apiproxy"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/web"
)

// PluginName is the canonical Connection Host Plugin name.
const PluginName = "@deepseek-ai/dsh-client-connection"

// PluginConfig is the validated Connection deployment configuration.
type PluginConfig struct {
	ListenAddress  string
	TrustedHosts   []string
	MaxBodyBytes   int64
	GracefulPeriod time.Duration
	ServeWeb       bool
}

// Plugin owns the Echo HTTP/WebSocket carrier and listening endpoint. It is
// intended for commit-phase activation and therefore provides no Service.
type Plugin struct {
	plugin.Base
	settings PluginConfig

	mutex    sync.RWMutex
	carrier  *HTTPHost
	listener net.Listener
	done     chan struct{}
	serveErr error
	address  string
}

// NewPlugin validates settings and constructs an inactive Connection Plugin.
func NewPlugin(settings PluginConfig) (*Plugin, error) {
	if strings.TrimSpace(settings.ListenAddress) == "" ||
		settings.ListenAddress != strings.TrimSpace(settings.ListenAddress) {
		return nil, errors.New(
			"connection: listen address must be non-empty and trimmed",
		)
	}
	if settings.MaxBodyBytes < 0 {
		return nil, errors.New("connection: max body bytes must not be negative")
	}
	if settings.GracefulPeriod < 0 {
		return nil, errors.New("connection: graceful period must not be negative")
	}
	settings.TrustedHosts = append([]string(nil), settings.TrustedHosts...)
	return &Plugin{
		settings: settings,
	}, nil
}

// Manifest declares the API Proxy and optional embedded Web dependencies.
func (owner *Plugin) Manifest() plugin.Manifest {
	requiredServices := []plugin.ServiceType{
		plugin.ServiceOf[apiproxy.Service](),
	}
	if owner.settings.ServeWeb {
		requiredServices = append(
			requiredServices,
			plugin.ServiceOf[web.Frontend](),
		)
	}
	return plugin.Manifest{
		Name:     PluginName,
		Requires: requiredServices,
	}
}

// Apply resolves transport dependencies, binds the endpoint synchronously,
// and starts serving for this Fiber's lifetime.
func (owner *Plugin) Apply(requestContext context.Context) error {
	if err := requestContext.Err(); err != nil {
		return err
	}
	proxy, err := plugin.Require[apiproxy.Service](owner)
	if err != nil {
		return err
	}
	var frontend web.Frontend
	if owner.settings.ServeWeb {
		frontend, err = plugin.Require[web.Frontend](owner)
		if err != nil {
			return err
		}
	}
	carrier, err := NewHTTPHost(
		HTTPConfig{
			TrustedHosts:    owner.settings.TrustedHosts,
			MaxBodyBytes:    owner.settings.MaxBodyBytes,
			GracefulTimeout: owner.settings.GracefulPeriod,
			Frontend:        frontend,
		},
		proxy,
		proxy,
	)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", owner.settings.ListenAddress)
	if err != nil {
		return err
	}
	done := make(chan struct{})
	owner.mutex.Lock()
	owner.carrier = carrier
	owner.listener = listener
	owner.done = done
	owner.serveErr = nil
	owner.address = listener.Addr().String()
	owner.mutex.Unlock()
	go func() {
		serveErr := carrier.Serve(plugin.Lifetime(owner), listener)
		owner.mutex.Lock()
		owner.serveErr = serveErr
		close(done)
		owner.mutex.Unlock()
	}()
	return requestContext.Err()
}

// Dispose waits for lifetime-driven graceful shutdown and force-closes the
// carrier only when the caller's stop deadline expires.
func (owner *Plugin) Dispose(closeContext context.Context) error {
	owner.mutex.RLock()
	carrier := owner.carrier
	listener := owner.listener
	done := owner.done
	owner.mutex.RUnlock()
	if done == nil {
		return nil
	}
	select {
	case <-done:
		owner.mutex.RLock()
		serveErr := owner.serveErr
		owner.mutex.RUnlock()
		owner.clearActivation(carrier, listener, done)
		return serveErr
	case <-closeContext.Done():
		forceErr := errors.Join(listener.Close(), carrier.Close(closeContext))
		<-done
		owner.mutex.RLock()
		serveErr := owner.serveErr
		owner.mutex.RUnlock()
		owner.clearActivation(carrier, listener, done)
		return errors.Join(closeContext.Err(), forceErr, serveErr)
	}
}

// BoundAddress returns the bound endpoint after successful activation.
func (owner *Plugin) BoundAddress() string {
	owner.mutex.RLock()
	address := owner.address
	owner.mutex.RUnlock()
	return address
}

func (owner *Plugin) clearActivation(
	carrier *HTTPHost,
	listener net.Listener,
	done chan struct{},
) {
	owner.mutex.Lock()
	if owner.carrier == carrier && owner.listener == listener && owner.done == done {
		owner.carrier = nil
		owner.listener = nil
		owner.done = nil
		owner.address = ""
	}
	owner.mutex.Unlock()
}
