package sessionadapter

import (
	"context"
	"errors"
	"time"

	agentregistry "github.com/gorenx/goren/agent/registry"
	sessionapi "github.com/gorenx/goren/apiproxy/session"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/session/title"
	titleactor "github.com/gorenx/goren/session/title/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Titles implements the title domain port using admitted Title component routes.
type Titles struct {
	system   goakt.ActorSystem
	registry *goakt.GrainIdentity
	timeout  time.Duration
}

// NewTitles binds the lifecycle directory without creating Title actors.
func NewTitles(system goakt.ActorSystem, registry *goakt.GrainIdentity, timeout time.Duration) (*Titles, error) {
	if system == nil || registry == nil || timeout <= 0 {
		return nil, errors.New("session API: Title directory and positive timeout required")
	}
	return &Titles{
		system:   system,
		registry: registry,
		timeout:  timeout,
	}, nil
}

// Rename returns the title committed by this operation, not a subsequent read.
func (source *Titles) Rename(ctx context.Context, identifier session.SessionID, text string) (sessionapi.Title, error) {
	payload, err := source.system.AskGrain(ctx, source.registry, agentregistry.Resolve{
		AgentID:   identifier,
		WaitReady: true,
	}, source.timeout)
	if err != nil {
		return sessionapi.Title{}, err
	}
	ready, ok := payload.(agentregistry.Ready)
	if !ok {
		return sessionapi.Title{}, errors.New("session API: invalid Title directory reply")
	}
	if ready.Err != nil {
		return sessionapi.Title{}, ready.Err
	}
	if ready.Creation.AgentID != identifier || ready.Creation.CreationID == "" {
		return sessionapi.Title{}, errors.New("session API: Title creation identity mismatch")
	}
	var endpoint *goakt.GrainIdentity
	for _, component := range ready.Components {
		if component.Runtime != nil && component.Runtime.Name() == titleactor.RuntimeName(string(identifier)) {
			endpoint = component.Runtime
			break
		}
	}
	if endpoint == nil {
		return sessionapi.Title{}, errors.New("session API: initialized Title endpoint is missing")
	}
	payload, err = source.system.AskGrain(ctx, endpoint, titleactor.Rename{
		CreationID: ready.Creation.CreationID,
		Title:      text,
	}, source.timeout)
	if err != nil {
		return sessionapi.Title{}, err
	}
	result, ok := payload.(titleactor.Updated)
	if !ok {
		return sessionapi.Title{}, errors.New("session API: invalid title commit reply")
	}
	if result.Err != nil {
		return sessionapi.Title{}, result.Err
	}
	snapshot, err := title.Fold(result.Result.Events)
	if err != nil {
		return sessionapi.Title{}, err
	}
	if snapshot == nil {
		return sessionapi.Title{}, errors.New("session API: title commit contains no title")
	}
	return sessionapi.Title{
		Text: snapshot.Title,
		Seq:  snapshot.EventSeq,
	}, nil
}

var _ sessionapi.Titles = (*Titles)(nil)
