package session

import (
	"context"
	"errors"

	"github.com/gorenx/goren/plugin"
)

// Plugin adapts one MemoryStore to the Plugin Runtime. It owns only the
// Service binding and Session event source; the Store owns Session state.
type Plugin struct {
	plugin.Base
	store *memoryStore
}

// NewPlugin constructs the canonical Session Plugin and its business Store.
func NewPlugin(options MemoryStoreOptions) (*Plugin, error) {
	owner := &Plugin{}
	store, err := newMemoryStore(options, owner)
	if err != nil {
		return nil, err
	}
	owner.store = store
	return owner, nil
}

// Manifest publishes the independent Session Store Service.
func (owner *Plugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: PluginName,
		Provides: []plugin.ProvidedService{
			plugin.NewProvidedService[LiveStore](owner.store),
		},
	}
}

// Apply validates startup cancellation before Service publication.
func (*Plugin) Apply(requestContext context.Context) error {
	if requestContext == nil {
		return errors.New("session: Plugin Apply Context is nil")
	}
	return requestContext.Err()
}

// Dispose asks the business Store to release every live Session.
func (owner *Plugin) Dispose(closeContext context.Context) error {
	if closeContext == nil {
		closeContext = context.Background()
	}
	return owner.store.Close(context.WithoutCancel(closeContext))
}

func (owner *Plugin) Publish(
	requestContext context.Context,
	fact plugin.Event,
) error {
	return plugin.PublishEvent(requestContext, owner, fact)
}

var _ plugin.Plugin = (*Plugin)(nil)
var _ eventPublisher = (*Plugin)(nil)
