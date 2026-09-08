package queryactor

import (
	"context"
	"errors"

	sessionactor "github.com/gorenx/goren/session/actor"
	"github.com/gorenx/goren/session/persistence"
	persistenceactor "github.com/gorenx/goren/session/persistence/actor"
	"github.com/gorenx/goren/session/query"
	sessionregistry "github.com/gorenx/goren/session/registry"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Collector acquires and publishes one complete observation, then stops.
// Its initiating Grain must serialize collections for Target and enable
// AllowAll request reentrancy when spawning this actor.
type Collector struct {
	Registry    *goakt.GrainIdentity
	Persistence *goakt.PID
	Target      *goakt.PID
	// Reply is detached from the initiating GrainContext by DeferResponse.
	Reply *goakt.GrainReply

	live     []sessionregistry.Session
	sources  []query.Source
	page     persistence.SnapshotPage
	position int
}

// PreStart rejects missing routes without acquiring storage or Session ownership.
func (owner *Collector) PreStart(*goakt.Context) error {
	if owner.Registry == nil || owner.Persistence == nil || owner.Target == nil || owner.Reply == nil {
		return errors.New("session query: collector routes and deferred reply are required")
	}
	return nil
}

// PostStop reports incomplete collection without stopping independent owners.
func (owner *Collector) PostStop(*goakt.Context) error {
	if owner.Reply != nil {
		owner.Reply.Response(Completion{
			Err: errors.New("session query: collector stopped before completion"),
		})
		owner.Reply = nil
	}
	return nil
}

// Receive advances collection only after the preceding request completes.
func (owner *Collector) Receive(rc *goakt.ReceiveContext) {
	switch input := rc.Message().(type) {
	case *goakt.PostStart:
		rc.RequestGrain(owner.Registry, sessionregistry.ListSessions{}, goakt.WithRequestTimeout(0)).Then(collectorReply(rc))
	case sessionregistry.Sessions:
		owner.live = input.Items
		owner.readLive(rc)
	case sessionactor.View:
		if input.Err != nil {
			owner.finish(rc, input.Err)
			return
		}
		entry := owner.live[len(owner.sources)]
		if input.Snapshot.Barrier.SessionID != entry.Header.ID {
			owner.finish(rc, errors.New("session query: live reply identity mismatch"))
			return
		}
		owner.sources = append(owner.sources, query.Source{
			Log: query.LogSnapshot{
				Header: entry.Header,
				Events: input.Snapshot.Events,
			},
			Live: true,
		})
		owner.readLive(rc)
	case persistenceactor.Snapshots:
		if input.Err != nil {
			owner.finish(rc, input.Err)
			return
		}
		owner.page = input.Value
		owner.position = 0
		owner.readStored(rc)
	case persistenceactor.Inspection:
		if input.Err != nil {
			owner.finish(rc, input.Err)
			return
		}
		expected := owner.page.Snapshots[owner.position].Header.ID
		if input.Value.Header.ID != expected {
			owner.finish(rc, errors.New("session query: persisted reply identity mismatch"))
			return
		}
		owner.sources = append(owner.sources, query.Source{
			Log: query.LogSnapshot{
				Header: input.Value.Header,
				Events: input.Value.Events,
			},
			Persisted: true,
		})
		owner.position++
		owner.readStored(rc)
	case Completion:
		owner.finish(rc, input.Err)
	case collectionFailed:
		owner.finish(rc, input.err)
	default:
		owner.finish(rc, errors.New("session query: unexpected collector response"))
	}
}

func (owner *Collector) readLive(rc *goakt.ReceiveContext) {
	if len(owner.sources) == len(owner.live) {
		owner.readPage(rc, nil)
		return
	}
	entry := owner.live[len(owner.sources)]
	identity, err := goakt.GrainOf[*sessionactor.Grain](rc.Context(), rc.ActorSystem(), sessionactor.RuntimeName(string(entry.Header.ID)), goakt.WithLongLivedGrain())
	if err != nil {
		owner.finish(rc, err)
		return
	}
	rc.RequestGrain(identity, sessionactor.Read{
		CreationID: entry.CreationID,
	}, goakt.WithRequestTimeout(0)).Then(collectorReply(rc))
}

func (owner *Collector) readPage(rc *goakt.ReceiveContext, cursor *persistence.SessionCursor) {
	rc.Request(owner.Persistence, persistenceactor.ListSnapshots{
		Page: persistence.SessionPage{
			Cursor: cursor,
			Limit:  128,
		},
	}, goakt.WithRequestTimeout(0)).Then(collectorReply(rc))
}

func (owner *Collector) readStored(rc *goakt.ReceiveContext) {
	for owner.position < len(owner.page.Snapshots) {
		entry := owner.page.Snapshots[owner.position]
		online := false
		for _, active := range owner.live {
			if active.Header.ID == entry.Header.ID {
				online = true
				break
			}
		}
		if online {
			// Observe validates matching headers and selects the complete live log.
			owner.sources = append(owner.sources, query.Source{
				Log: query.LogSnapshot{
					Header: entry.Header,
				},
				Persisted: true,
			})
			owner.position++
			continue
		}
		rc.Request(owner.Persistence, persistenceactor.Inspect{
			SessionID: entry.Header.ID,
		}, goakt.WithRequestTimeout(0)).Then(collectorReply(rc))
		return
	}
	if owner.page.NextCursor != nil {
		owner.readPage(rc, owner.page.NextCursor)
		return
	}
	rc.Request(owner.Target, Observe{
		Sources: owner.sources,
	}, goakt.WithRequestTimeout(0)).Then(collectorReply(rc))
}

func (owner *Collector) finish(rc *goakt.ReceiveContext, failure error) {
	owner.Reply.Response(Completion{
		Err: failure,
	})
	owner.Reply = nil
	rc.Shutdown()
}

type collectionFailed struct {
	err error
}

// collectorReply captures stable values only; ReceiveContext is pooled.
func collectorReply(rc *goakt.ReceiveContext) func(any, error) {
	ctx := context.WithoutCancel(rc.Context())
	self := rc.Self()
	return func(payload any, failure error) {
		if failure != nil {
			payload = collectionFailed{
				err: failure,
			}
		}
		if err := goakt.Tell(ctx, self, payload); err != nil {
			self.ActorSystem().Logger().Errorf("session query: deliver collection reply: %v", err)
		}
	}
}
