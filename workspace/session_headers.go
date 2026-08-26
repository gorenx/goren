package workspace

import (
	"context"
	"errors"

	"github.com/gorenx/goren/session"
	sesspersist "github.com/gorenx/goren/session/persistence"
)

// sessionHeaderSource translates Session ownership into the immutable header
// port consumed by Workspace.
type sessionHeaderSource struct {
	sessions    session.LiveStore
	persistence sesspersist.Persistence
}

func (source *sessionHeaderSource) Get(
	requestContext context.Context,
	identifier session.SessionID,
) (session.Header, bool, error) {
	if conversation, found := source.sessions.Get(identifier); found {
		return conversation.Header(), true, nil
	}
	loaded, err := source.persistence.Inspect(requestContext, identifier)
	if err != nil {
		var missing *sesspersist.NotFoundError
		if errors.As(err, &missing) {
			return session.Header{}, false, nil
		}
		return session.Header{}, false, err
	}
	return loaded.Header, true, nil
}

func (source *sessionHeaderSource) List(
	requestContext context.Context,
) ([]session.Header, error) {
	byID := make(map[session.SessionID]session.Header)
	order := make([]session.SessionID, 0)
	var cursor *sesspersist.SessionCursor
	for {
		page, err := source.persistence.List(
			requestContext,
			sesspersist.SessionPage{
				Cursor: cursor,
				Limit:  256,
			},
		)
		if err != nil {
			return nil, err
		}
		for _, header := range page.Headers {
			byID[header.ID] = header
			order = append(order, header.ID)
		}
		if page.NextCursor == nil {
			break
		}
		cursor = page.NextCursor
	}
	for _, conversation := range source.sessions.List() {
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
