package sessionadapter

import (
	"context"
	"errors"
	"time"

	sessionapi "github.com/gorenx/goren/apiproxy/session"
	"github.com/gorenx/goren/session/query"
	queryactor "github.com/gorenx/goren/session/query/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Search submits API search requests to the existing Query owner.
// It neither observes sources nor opens or updates the derived index.
type Search struct {
	target  *goakt.PID
	timeout time.Duration
}

// NewSearch binds a Query actor started by composition.
func NewSearch(target *goakt.PID, timeout time.Duration) (*Search, error) {
	if target == nil || timeout <= 0 {
		return nil, errors.New("session API: Query endpoint and positive timeout required")
	}
	return &Search{
		target:  target,
		timeout: timeout,
	}, nil
}

// SearchSessions returns the Query owner's ranked page and preserves its errors.
func (source *Search) SearchSessions(ctx context.Context, input query.SearchSessionsRequest) (query.SessionSearchPage, error) {
	payload, err := goakt.Ask(ctx, source.target, queryactor.SearchSessions{
		Request: input,
	}, source.timeout)
	if err != nil {
		return query.SessionSearchPage{}, err
	}
	result, ok := payload.(queryactor.SessionMatches)
	if !ok {
		return query.SessionSearchPage{}, errors.New("session API: invalid Query search reply")
	}
	return result.Page, result.Err
}

var _ sessionapi.Search = (*Search)(nil)
