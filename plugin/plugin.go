// Package plugin provides a statically linked, scoped Plugin Runtime with
// typed Services, Events, and Waterfalls.
package plugin

import (
	"context"
	"errors"
	"sync"
)

// Base is the opaque activation anchor embedded by every Plugin. It does not
// expose Scope, registries, or lifecycle mutation to business methods.
type Base struct {
	mutex sync.RWMutex
	fiber *fiber
}

// RuntimePlugin returns the embedded activation anchor.
func (pluginBase *Base) RuntimePlugin() *Base {
	return pluginBase
}

func (pluginBase *Base) attach(running *fiber) error {
	if pluginBase == nil {
		return errors.New("plugin: Plugin returned a nil Base")
	}
	pluginBase.mutex.Lock()
	defer pluginBase.mutex.Unlock()
	if pluginBase.fiber != nil {
		return errors.New("plugin: Plugin instance is already mounted")
	}
	pluginBase.fiber = running
	return nil
}

func (pluginBase *Base) detach(running *fiber) {
	if pluginBase == nil {
		return
	}
	pluginBase.mutex.Lock()
	if pluginBase.fiber == running {
		pluginBase.fiber = nil
	}
	pluginBase.mutex.Unlock()
}

func (pluginBase *Base) currentFiber() *fiber {
	if pluginBase == nil {
		return nil
	}
	pluginBase.mutex.RLock()
	running := pluginBase.fiber
	pluginBase.mutex.RUnlock()
	return running
}

// Plugin is one statically linked runtime participant. Apply acquires the
// Plugin's own resources after Runtime has resolved its declared dependencies.
// Dispose must be idempotent and tolerate a partially completed Apply.
type Plugin interface {
	RuntimePlugin() *Base
	Manifest() Manifest
	Apply(context.Context) error
	Dispose(context.Context) error
}

func fiberOf(owner Plugin) (*fiber, error) {
	if owner == nil || owner.RuntimePlugin() == nil {
		return nil, ErrPluginNotBound
	}
	running := owner.RuntimePlugin().currentFiber()
	if running == nil || running.runtime == nil || running.mount == nil {
		return nil, ErrPluginNotBound
	}
	return running, nil
}

func activeFiberOf(owner Plugin) (*fiber, error) {
	running, err := fiberOf(owner)
	if err != nil {
		return nil, err
	}
	running.runtime.view.RLock()
	active := running.state == FiberActive
	running.runtime.view.RUnlock()
	if !active {
		return nil, ErrPluginNotActive
	}
	return running, nil
}

var closedLifetime = func() context.Context {
	closedContext, cancelLifetime := context.WithCancelCause(context.Background())
	cancelLifetime(ErrPluginNotBound)
	return closedContext
}()

// Lifetime returns the current Plugin activation lifetime.
func Lifetime(owner Plugin) context.Context {
	running, err := fiberOf(owner)
	if err != nil {
		return closedLifetime
	}
	running.runtime.view.RLock()
	fiberLifetime := running.lifetime
	running.runtime.view.RUnlock()
	if fiberLifetime == nil {
		return closedLifetime
	}
	return fiberLifetime
}
