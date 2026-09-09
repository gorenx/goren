package cacheactor

import (
	"context"
	"errors"

	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/session/projectioncache"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

// RegistryName identifies the process-wide cache lifecycle owner.
const RegistryName = "projection-caches"

// ConfigureRegistry installs a detached startup checkpoint baseline and borrows storage.
type ConfigureRegistry struct {
	Storage projectioncache.CheckpointStore
	Records map[session.SessionID]projectioncache.CheckpointRecord
}

// CacheForSession requests an initialized cache route for a live or cold Session.
type CacheForSession struct{ SessionID session.SessionID }

// CacheEndpoint returns a ready cache; an error exposes no usable route.
type CacheEndpoint struct {
	Target *goakt.GrainIdentity
	Err    error
}

// CloseRegistry rejects further acquisition and drains every acquired cache.
// Shared checkpoint storage remains owned by the process composition.
type CloseRegistry struct{}

// Registry owns per-Session cache initialization and shutdown, never checkpoint
// policy or projection computation. It also serves Sessions without a live Agent.
type Registry struct {
	settings *ConfigureRegistry
	// entries records acquired identities, including uncertain initialization.
	entries map[session.SessionID]*cacheEntry
	closing bool
}

type cacheEntry struct {
	identity *goakt.GrainIdentity
	failure  error
}

// OnActivate leaves configuration and resource acquisition to explicit messages.
func (*Registry) OnActivate(context.Context, *goakt.GrainProps) error { return nil }

// OnDeactivate does not claim cleanup; the process must await CloseRegistry first.
func (*Registry) OnDeactivate(context.Context, *goakt.GrainProps) error { return nil }

// OnReceive serializes lifecycle requests; checkpoint business remains in Grain.
func (owner *Registry) OnReceive(gc *goakt.GrainContext) {
	switch input := gc.Message().(type) {
	case ConfigureRegistry:
		if owner.settings != nil || input.Storage == nil {
			gc.Response(Completion{Err: errors.New("projection cache: fresh registry and storage required")})
			return
		}
		records := make(map[session.SessionID]projectioncache.CheckpointRecord, len(input.Records))
		for identifier, record := range input.Records {
			detached, failure := projectioncache.ValidateCheckpointRecord(identifier, record)
			if failure != nil {
				gc.Response(Completion{Err: failure})
				return
			}
			records[identifier] = detached
		}
		input.Records = records
		err := gc.EnableReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.StashNonReentrant)))
		if err == nil {
			owner.settings = &input
			owner.entries = make(map[session.SessionID]*cacheEntry)
		}
		gc.Response(Completion{Err: err})
	case CacheForSession:
		owner.acquire(gc, input)
	case CloseRegistry:
		owner.close(gc)
	default:
		gc.Unhandled()
	}
}

func (owner *Registry) acquire(gc *goakt.GrainContext, input CacheForSession) {
	if owner.settings == nil || owner.closing || input.SessionID == "" {
		gc.Response(CacheEndpoint{Err: errors.New("projection cache: registry unavailable or SessionID empty")})
		return
	}
	if entry := owner.entries[input.SessionID]; entry != nil {
		result := CacheEndpoint{Err: entry.failure}
		if result.Err == nil {
			result.Target = entry.identity
		}
		gc.Response(result)
		return
	}
	identity, err := goakt.GrainOf[*Grain](
		gc.Context(),
		gc.ActorSystem(),
		RuntimeName(string(input.SessionID)), goakt.WithLongLivedGrain(),
	)
	if err != nil {
		gc.Response(CacheEndpoint{Err: err})
		return
	}
	entry := &cacheEntry{identity: identity}
	owner.entries[input.SessionID] = entry
	initial := Initialize{
		SessionID: input.SessionID,
		Storage:   owner.settings.Storage,
	}
	if record, found := owner.settings.Records[input.SessionID]; found {
		initial.Record = &record
	}
	reply := gc.DeferResponse()
	gc.RequestGrain(
		identity,
		initial,
		goakt.WithRequestTimeout(0),
	).Then(
		func(payload any, failure error) {
			if failure == nil {
				result, ok := payload.(Completion)
				if !ok {
					failure = errors.New("projection cache: invalid initialization reply")
				} else {
					failure = result.Err
				}
			}
			entry.failure = failure
			result := CacheEndpoint{Err: failure}
			if failure == nil {
				result.Target = identity
			}
			reply.Response(result)
		},
	)
}

func (owner *Registry) close(gc *goakt.GrainContext) {
	owner.closing = true
	if len(owner.entries) == 0 {
		gc.Response(Completion{})
		return
	}
	reply := gc.DeferResponse()
	remaining := len(owner.entries)
	var failures []error
	for identifier, entry := range owner.entries {
		gc.RequestGrain(
			entry.identity, Close{}, goakt.WithRequestTimeout(0),
		).Then(
			func(payload any, failure error) {
				if failure == nil {
					result, valid := payload.(Completion)
					if !valid {
						failure = errors.New("projection cache: invalid close reply")
					} else {
						failure = result.Err
					}
				}
				if failure != nil {
					failures = append(failures, failure)
				} else {
					delete(owner.entries, identifier)
				}
				remaining--
				if remaining == 0 {
					reply.Response(Completion{Err: errors.Join(failures...)})
				}
			},
		)
	}
}
