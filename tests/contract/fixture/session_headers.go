//go:build contract

package fixture

import (
	"context"
	"errors"

	"github.com/gorenx/goren/session"
	sessionpersistence "github.com/gorenx/goren/session/persistence"
)

// SessionHeaderSource merges live and persisted immutable headers for Workspace tests.
type SessionHeaderSource struct {
	Sessions    session.Store
	Persistence sessionpersistence.Persistence
}

func (source SessionHeaderSource) Get(
	requestContext context.Context,
	identifier session.SessionID,
) (session.Header, bool, error) {
	if conversation, found := source.Sessions.Get(identifier); found {
		return conversation.Header(), true, nil
	}
	loaded, err := source.Persistence.Inspect(requestContext, identifier)
	if err != nil {
		var missing *sessionpersistence.NotFoundError
		if errors.As(err, &missing) {
			return session.Header{}, false, nil
		}
		return session.Header{}, false, err
	}
	return loaded.Header, true, nil
}

func (source SessionHeaderSource) List(requestContext context.Context) ([]session.Header, error) {
	stored, err := source.Persistence.List(requestContext)
	if err != nil {
		return nil, err
	}
	byID := make(map[session.SessionID]session.Header, len(stored))
	order := make([]session.SessionID, 0, len(stored))
	for _, header := range stored {
		byID[header.ID] = header
		order = append(order, header.ID)
	}
	for _, conversation := range source.Sessions.List() {
		header := conversation.Header()
		if _, found := byID[header.ID]; !found {
			order = append(order, header.ID)
		}
		byID[header.ID] = header
	}
	result := make([]session.Header, 0, len(order))
	for _, identifier := range order {
		result = append(result, byID[identifier])
	}
	return result, nil
}
