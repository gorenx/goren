// Package eventadapter connects Session event projections to existing Actor owners.
package eventadapter

import (
	"context"
	"errors"
	"time"

	sessionevents "github.com/gorenx/goren/apiproxy/session/events"
	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	sessionregistry "github.com/gorenx/goren/session/registry"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Snapshots reads committed Session facts without creating actors or retaining logs.
// Methods run on inbound goroutines, never within an Actor receive turn.
type Snapshots struct {
	system   goakt.ActorSystem
	registry *goakt.GrainIdentity
	timeout  time.Duration
}

// NewSnapshots binds the existing Session Registry and a bounded request timeout.
func NewSnapshots(system goakt.ActorSystem, registry *goakt.GrainIdentity, timeout time.Duration) (*Snapshots, error) {
	if system == nil ||
		registry == nil ||
		timeout <= 0 {
		return nil, errors.New("session API: registry and positive read timeout are required")
	}
	return &Snapshots{
		system:   system,
		registry: registry,
		timeout:  timeout,
	}, nil
}

// ListSnapshots captures each admitted Session's committed prefix.
// A creation proven stale during the read is omitted; delivery failures propagate.
func (source *Snapshots) ListSnapshots(ctx context.Context) ([]sessionevents.Snapshot, error) {
	payload, err := source.system.AskGrain(ctx, source.registry, sessionregistry.ListSessions{}, source.timeout)
	if err != nil {
		return nil, err
	}
	listing, ok := payload.(sessionregistry.Sessions)
	if !ok {
		return nil, errors.New("session API: invalid Session listing reply")
	}
	result := make([]sessionevents.Snapshot, 0, len(listing.Items))
	for _, entry := range listing.Items {
		snapshot, err := source.ReadSnapshot(ctx, entry.Header.ID, entry.CreationID)
		if errors.Is(err, sessionactor.ErrCreation) {
			continue
		}
		if err != nil {
			return nil, err
		}
		result = append(result, snapshot)
	}
	return result, nil
}

// ReadSnapshot reads only the requested creation; a replacement cannot satisfy it.
func (source *Snapshots) ReadSnapshot(ctx context.Context, identifier session.SessionID, creationID string) (sessionevents.Snapshot, error) {
	payload, err := source.system.AskGrain(ctx, source.registry, sessionregistry.ResolveSession{
		SessionID:  identifier,
		CreationID: creationID,
	}, source.timeout)
	if err != nil {
		return sessionevents.Snapshot{}, err
	}
	endpoint, ok := payload.(sessionregistry.Endpoint)
	if !ok {
		return sessionevents.Snapshot{}, errors.New("session API: invalid Session endpoint reply")
	}
	if endpoint.Err != nil {
		return sessionevents.Snapshot{}, endpoint.Err
	}
	if endpoint.Target == nil {
		return sessionevents.Snapshot{}, errors.New("session API: Session endpoint is absent")
	}
	payload, err = source.system.AskGrain(ctx, endpoint.Target, sessionactor.Read{
		CreationID: creationID,
	}, source.timeout)
	if err != nil {
		return sessionevents.Snapshot{}, err
	}
	view, ok := payload.(sessionactor.View)
	if !ok {
		return sessionevents.Snapshot{}, errors.New("session API: invalid Session snapshot reply")
	}
	if view.Err != nil {
		return sessionevents.Snapshot{}, view.Err
	}
	if view.Header.ID != identifier {
		return sessionevents.Snapshot{}, errors.New("session API: snapshot belongs to another Session")
	}
	return sessionevents.Snapshot{
		CreationID: creationID,
		Header:     view.Header,
		Events:     view.Snapshot.Events,
	}, nil
}

var _ sessionevents.Snapshots = (*Snapshots)(nil)
