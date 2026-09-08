package sessionadapter

import (
	"context"
	"errors"
	"time"

	sessionapi "github.com/gorenx/goren/apiproxy/session"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/session/projection"
	projectionactor "github.com/gorenx/goren/session/projection/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Projections implements projection reads over explicit Session prefixes.
type Projections struct {
	system   goakt.ActorSystem
	endpoint *goakt.GrainIdentity
	timeout  time.Duration
}

// NewProjections binds the shared registered projection definitions.
func NewProjections(system goakt.ActorSystem, endpoint *goakt.GrainIdentity, timeout time.Duration) (*Projections, error) {
	if system == nil || endpoint == nil || timeout <= 0 {
		return nil, errors.New("session API: projection endpoint and positive timeout required")
	}
	return &Projections{
		system:   system,
		endpoint: endpoint,
		timeout:  timeout,
	}, nil
}

// Read computes the supplied prefix without changing another live creation's cache.
func (source *Projections) Read(ctx context.Context, entries []session.Event) (projection.Snapshot, error) {
	payload, err := source.system.AskGrain(ctx, source.endpoint, projectionactor.Compute{
		Events: entries,
	}, source.timeout)
	if err != nil {
		return projection.Snapshot{}, err
	}
	result, ok := payload.(projectionactor.Viewed)
	if !ok {
		return projection.Snapshot{}, errors.New("session API: invalid projection reply")
	}
	return result.Snapshot, result.Err
}

var _ sessionapi.Projections = (*Projections)(nil)
