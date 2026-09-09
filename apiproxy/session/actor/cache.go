package sessionadapter

import (
	"context"
	"errors"
	"time"

	sessionapi "github.com/gorenx/goren/apiproxy/session"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/session/projection"
	"github.com/gorenx/goren/session/projectioncache"
	cacheactor "github.com/gorenx/goren/session/projectioncache/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Cache binds API reads to the existing ProjectionCache owners. Registry handles
// lifecycle; this client never starts a Grain or reconstructs a projection itself.
type Cache struct {
	system      goakt.ActorSystem
	registry    *goakt.GrainIdentity
	projections *goakt.GrainIdentity
	history     projectioncache.DurableEventReader
	timeout     time.Duration
}

// NewCache borrows initialized process routes and the durable Session reader.
func NewCache(system goakt.ActorSystem, registry *goakt.GrainIdentity, projections *goakt.GrainIdentity, history projectioncache.DurableEventReader, timeout time.Duration) (*Cache, error) {
	if system == nil || registry == nil || projections == nil || history == nil || timeout <= 0 {
		return nil, errors.New("session API: cache routes, durable reader and positive timeout required")
	}
	return &Cache{
		system:      system,
		registry:    registry,
		projections: projections,
		history:     history,
		timeout:     timeout,
	}, nil
}

func (source *Cache) endpoint(ctx context.Context, identifier session.SessionID) (*goakt.GrainIdentity, error) {
	payload, err := source.system.AskGrain(ctx, source.registry, cacheactor.CacheForSession{
		SessionID: identifier,
	}, source.timeout)
	if err != nil {
		return nil, err
	}
	result, valid := payload.(cacheactor.CacheEndpoint)
	if !valid {
		return nil, errors.New("session API: invalid cache acquisition reply")
	}
	if result.Err != nil {
		return nil, result.Err
	}
	if result.Target == nil {
		return nil, errors.New("session API: cache route is absent")
	}
	return result.Target, nil
}

// CachedSnapshot returns a confirmed saved view; nil without error is a cache miss.
func (source *Cache) CachedSnapshot(ctx context.Context, metadata session.Header) (*projection.Snapshot, error) {
	target, err := source.endpoint(ctx, metadata.ID)
	if err != nil {
		return nil, err
	}
	payload, err := source.system.AskGrain(ctx, target, cacheactor.CachedSnapshot{
		Header:     metadata,
		Projection: source.projections,
	}, source.timeout)
	if err != nil {
		return nil, err
	}
	result, valid := payload.(cacheactor.Snapshot)
	if !valid {
		return nil, errors.New("session API: invalid cached snapshot reply")
	}
	return result.Value, result.Err
}

// ColdSnapshot delegates durable replay and Unit restoration to ProjectionCache.
func (source *Cache) ColdSnapshot(ctx context.Context, identifier session.SessionID) (projection.Snapshot, error) {
	target, err := source.endpoint(ctx, identifier)
	if err != nil {
		return projection.Snapshot{}, err
	}
	payload, err := source.system.AskGrain(ctx, target, cacheactor.ColdSnapshot{
		Storage:    source.history,
		Projection: source.projections,
	}, source.timeout)
	if err != nil {
		return projection.Snapshot{}, err
	}
	result, valid := payload.(cacheactor.ColdView)
	if !valid {
		return projection.Snapshot{}, errors.New("session API: invalid cold snapshot reply")
	}
	if result.Err != nil {
		return projection.Snapshot{}, result.Err
	}
	if result.Value == nil || result.Value.Header.ID != identifier {
		return projection.Snapshot{}, errors.New("session API: cold snapshot identity mismatch")
	}
	return result.Value.Snapshot, nil
}

var _ sessionapi.ProjectionCache = (*Cache)(nil)
