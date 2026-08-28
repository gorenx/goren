// Package plugin adapts Session Projection Cache to the repository Plugin Runtime.
package plugin

import (
	"context"
	"errors"

	pluginruntime "github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/session/persistence"
	"github.com/gorenx/goren/session/projection"
	"github.com/gorenx/goren/session/projectioncache"
)

// StoreOpener acquires the configured checkpoint Store during Plugin Apply.
type StoreOpener interface {
	OpenCheckpointStore(context.Context) (projectioncache.CheckpointStore, error)
}

// Plugin owns dependency resolution and translates Runtime events into
// projectioncache business inputs. It owns no checkpoint state or policy.
type Plugin struct {
	pluginruntime.Base
	opener      StoreOpener
	coordinator *projectioncache.Coordinator
}

// New constructs an inactive Plugin and its stable published Cache object.
func New(
	opener StoreOpener,
	settings projectioncache.Config,
) (*Plugin, error) {
	if opener == nil {
		return nil, errors.New("session projection cache plugin: StoreOpener is required")
	}
	cacheCoordinator, err := projectioncache.New(settings)
	if err != nil {
		return nil, err
	}
	return &Plugin{
		opener:      opener,
		coordinator: cacheCoordinator,
	}, nil
}

// Manifest publishes the read Cache and declares its Session lifecycle inputs.
func (owner *Plugin) Manifest() pluginruntime.Manifest {
	return pluginruntime.Manifest{
		Name: projectioncache.PluginName,
		Provides: []pluginruntime.ProvidedService{
			pluginruntime.NewProvidedService[projectioncache.Cache](owner.coordinator),
		},
		Requires: []pluginruntime.ServiceType{
			pluginruntime.ServiceOf[session.LiveStore](),
			pluginruntime.ServiceOf[persistence.Persistence](),
			pluginruntime.ServiceOf[projection.Registry](),
		},
		Events: []pluginruntime.EventSubscription{
			pluginruntime.EventOf[session.Created](),
			pluginruntime.EventOf[session.EventAppended](),
			pluginruntime.EventOf[session.Disposed](),
		},
	}
}

// Apply opens storage and activates the ordinary Go cache object.
func (owner *Plugin) Apply(requestContext context.Context) error {
	if requestContext == nil {
		return errors.New("session projection cache plugin: Apply Context is nil")
	}
	sessions, err := pluginruntime.Require[session.LiveStore](owner)
	if err != nil {
		return err
	}
	durability, err := pluginruntime.Require[persistence.Persistence](owner)
	if err != nil {
		return err
	}
	projections, err := pluginruntime.Require[projection.Registry](owner)
	if err != nil {
		return err
	}
	store, err := owner.opener.OpenCheckpointStore(requestContext)
	if err != nil {
		return err
	}
	if store == nil {
		return errors.New("session projection cache plugin: StoreOpener returned nil Store")
	}
	if err := owner.coordinator.Open(
		requestContext,
		sessions,
		durability,
		projections,
		store,
	); err != nil {
		return errors.Join(
			err,
			store.Close(context.WithoutCancel(requestContext)),
		)
	}
	return nil
}

// ObserveEvent translates Runtime Events into cache business inputs only.
func (owner *Plugin) ObserveEvent(
	_ context.Context,
	fact pluginruntime.Event,
) error {
	switch observed := fact.(type) {
	case session.Created:
		return owner.coordinator.Begin(observed.Conversation)
	case session.EventAppended:
		return owner.coordinator.Advance(observed.Conversation, observed.Committed)
	case session.Disposed:
		return owner.coordinator.Retire(observed.Conversation)
	default:
		return nil
	}
}

// Dispose closes cache admission and drains entered checkpoint work.
func (owner *Plugin) Dispose(closeContext context.Context) error {
	return owner.coordinator.Close(closeContext)
}

var _ pluginruntime.Plugin = (*Plugin)(nil)
