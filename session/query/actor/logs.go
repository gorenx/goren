package queryactor

import (
	"context"
	"errors"
	"time"

	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	"github.com/gorenx/goren/session/persistence"
	"github.com/gorenx/goren/session/query"
	sessionregistry "github.com/gorenx/goren/session/registry"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// History supplies validated durable pages and logs without owning live sessions.
type History interface {
	ListSnapshots(context.Context, persistence.SessionPage) (persistence.SnapshotPage, error)
	Inspect(context.Context, session.SessionID) (persistence.Inspection, error)
}

// Logs acquires detached observations outside actor Receive turns. Query Service
// still decides live precedence and rejects conflicting headers before publication.
type Logs struct {
	system   goakt.ActorSystem
	registry *goakt.GrainIdentity
	history  History
	timeout  time.Duration
}

// NewLogs binds source routes without creating actors or opening storage.
func NewLogs(system goakt.ActorSystem, registry *goakt.GrainIdentity, durable History, timeout time.Duration) (*Logs, error) {
	if system == nil ||
		registry == nil ||
		durable == nil ||
		timeout <= 0 {
		return nil, errors.New("session query: Registry, history and positive timeout are required")
	}
	return &Logs{
		system:   system,
		registry: registry,
		history:  durable,
		timeout:  timeout,
	}, nil
}

// ReadSources returns nothing publishable until every live read and durable page
// succeeds. An online identity is read using its captured creation, never rebound.
func (source *Logs) ReadSources(ctx context.Context) ([]query.Source, error) {
	if ctx == nil {
		return nil, errors.New("session query: source context is required")
	}
	err := ctx.Err()
	if err != nil {
		return nil, err
	}
	payload, err := source.system.AskGrain(ctx, source.registry, sessionregistry.ListSessions{}, source.timeout)
	if err != nil {
		return nil, err
	}
	listing, ok := payload.(sessionregistry.Sessions)
	if !ok {
		return nil, errors.New("session query: invalid Session list reply")
	}
	var collected []query.Source
	online := make(map[session.SessionID]struct{}, len(listing.Items))
	for _, entry := range listing.Items {
		identity, failure := goakt.GrainOf[*sessionactor.Grain](ctx, source.system,
			sessionactor.RuntimeName(string(entry.Header.ID)), goakt.WithLongLivedGrain())
		if failure != nil {
			return nil, failure
		}
		payload, failure = source.system.AskGrain(ctx, identity, sessionactor.Read{
			CreationID: entry.CreationID,
		}, source.timeout)
		if failure != nil {
			return nil, failure
		}
		view, valid := payload.(sessionactor.View)
		if !valid {
			return nil, errors.New("session query: invalid Session log reply")
		}
		if view.Err != nil {
			return nil, view.Err
		}
		if view.Snapshot.Barrier.SessionID != entry.Header.ID {
			return nil, errors.New("session query: live reply identity mismatch")
		}
		collected = append(collected, query.Source{
			Log: query.LogSnapshot{
				Header: entry.Header,
				Events: view.Snapshot.Events,
			},
			Live: true,
		})
		online[entry.Header.ID] = struct{}{}
	}
	var cursor *persistence.SessionCursor
	for {
		page, failure := source.history.ListSnapshots(
			ctx,
			persistence.SessionPage{
				Cursor: cursor,
				Limit:  128,
			},
		)
		if failure != nil {
			return nil, failure
		}
		for _, entry := range page.Snapshots {
			snapshot := query.LogSnapshot{
				Header: entry.Header,
			}
			_, live := online[entry.Header.ID]
			if !live {
				stored, failure := source.history.Inspect(ctx, entry.Header.ID)
				if failure != nil {
					return nil, failure
				}
				if stored.Header.ID != entry.Header.ID {
					return nil, errors.New("session query: persisted reply identity mismatch")
				}
				snapshot = query.LogSnapshot{
					Header: stored.Header,
					Events: stored.Events,
				}
			}
			collected = append(collected, query.Source{
				Log:       snapshot,
				Persisted: true,
			})
		}
		if page.NextCursor == nil {
			return collected, nil
		}
		cursor = page.NextCursor
	}
}

var _ query.Sources = (*Logs)(nil)
