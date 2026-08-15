package apiproxy

import (
	"context"
	"errors"
	"unicode/utf8"

	"github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/session"
	sessionquery "github.com/gorenx/goren/session/query"
)

const (
	sessionSearchResultLimit       = 20
	sessionSearchSnippetMaximum    = 240
	sessionSearchProviderCallLimit = 1_000
)

// SessionVisibility is the authorization boundary consumed by Session
// Search. It deliberately exposes identities instead of session.list DTOs.
type SessionVisibility interface {
	VisibleSessionIDs(context.Context) (map[session.SessionID]struct{}, error)
}

// SessionSearchGateway owns the session.search use case: fixed searchable
// event scope, list-equivalent visibility, cursor recovery, and wire mapping.
// Query observation and full-text mechanics remain in session/query.
type SessionSearchGateway struct {
	queries    sessionquery.QueryService
	visibility SessionVisibility
}

// NewSessionSearchGateway binds Session Query to the API authorization view.
func NewSessionSearchGateway(
	queries sessionquery.QueryService,
	visibility SessionVisibility,
) (*SessionSearchGateway, error) {
	if queries == nil || visibility == nil {
		return nil, errors.New("apiproxy: Session Search Gateway dependencies are incomplete")
	}
	return &SessionSearchGateway{queries: queries, visibility: visibility}, nil
}

// Search enforces list-equivalent visibility and projects the fixed current
// user/assistant message surface into the source wire result.
func (owner *SessionSearchGateway) Search(
	requestContext context.Context,
	call Request[SessionSearchRequest],
) (Outcome[SessionSearchValue], error) {
	cancelled := func() Outcome[SessionSearchValue] {
		return Fail[SessionSearchValue](newRPCError(
			connection.ErrorCancelled, "session search was aborted", struct{}{},
		))
	}
	if requestContext.Err() != nil {
		return cancelled(), nil
	}
	visibleIDs, err := owner.visibility.VisibleSessionIDs(requestContext)
	if err != nil {
		return Outcome[SessionSearchValue]{}, err
	}
	if len(visibleIDs) == 0 {
		return OK(SessionSearchValue{Items: []SessionSearchItem{}}), nil
	}
	authorized := make([]SessionSearchItem, 0, sessionSearchResultLimit+1)
	accepted := make(map[session.SessionID]struct{}, sessionSearchResultLimit+1)
	seenCursors := make(map[sessionquery.Cursor]struct{})
	var cursor sessionquery.Cursor
	providerCalls := 0
	staleRestarts := 0
	for len(authorized) <= sessionSearchResultLimit {
		if requestContext.Err() != nil {
			return cancelled(), nil
		}
		providerCalls++
		if providerCalls > sessionSearchProviderCallLimit {
			return Outcome[SessionSearchValue]{}, errors.New("apiproxy: Session search exceeded provider call budget")
		}
		page, searchErr := owner.queries.SearchSessions(requestContext, sessionquery.SearchSessionsRequest{
			Text: call.Payload.Query,
			Events: sessionquery.EventConstraints{
				Types:    []string{session.UserMessageEventName, session.AssistantMessageEventName},
				Surfaces: []sessionquery.Surface{sessionquery.SurfaceCurrent},
			},
			Limit: sessionSearchResultLimit, Cursor: cursor,
		})
		if searchErr != nil {
			var classified *sessionquery.Error
			if errors.As(searchErr, &classified) && classified.Code == sessionquery.ErrorStaleCursor && cursor != "" && staleRestarts == 0 {
				authorized = authorized[:0]
				clear(accepted)
				clear(seenCursors)
				cursor = ""
				staleRestarts++
				continue
			}
			if requestContext.Err() != nil || errors.As(searchErr, &classified) && classified.Code == sessionquery.ErrorAborted {
				return cancelled(), nil
			}
			return Fail[SessionSearchValue](newRPCError(
				connection.ErrorInternal, "session search failed: "+searchErr.Error(), struct{}{},
			)), nil
		}
		if len(page.Items) > sessionSearchResultLimit {
			return Outcome[SessionSearchValue]{}, errors.New("apiproxy: Session Query returned too many search hits")
		}
		for _, hit := range page.Items {
			if len(authorized) > sessionSearchResultLimit {
				break
			}
			identifier := hit.Header.ID
			if _, visible := visibleIDs[identifier]; !visible || hit.BestMatch.SessionID != identifier ||
				hit.BestMatch.Surface != sessionquery.SurfaceCurrent ||
				(hit.BestMatch.Type != session.UserMessageEventName && hit.BestMatch.Type != session.AssistantMessageEventName) {
				continue
			}
			if _, duplicate := accepted[identifier]; duplicate {
				continue
			}
			accepted[identifier] = struct{}{}
			authorized = append(authorized, SessionSearchItem{
				SessionID: SessionID(identifier),
				Snippet:   truncateCodePoints(hit.BestMatch.Snippet, sessionSearchSnippetMaximum),
			})
		}
		if len(authorized) > sessionSearchResultLimit || page.NextCursor == "" {
			break
		}
		if _, repeated := seenCursors[page.NextCursor]; repeated {
			return Outcome[SessionSearchValue]{}, errors.New("apiproxy: Session Query repeated a continuation cursor")
		}
		seenCursors[page.NextCursor] = struct{}{}
		cursor = page.NextCursor
	}
	hasMore := len(authorized) > sessionSearchResultLimit
	if hasMore {
		authorized = authorized[:sessionSearchResultLimit]
	}
	if authorized == nil {
		authorized = []SessionSearchItem{}
	}
	return OK(SessionSearchValue{Items: authorized, HasMore: hasMore}), nil
}

func truncateCodePoints(textValue string, maximum int) string {
	if utf8.RuneCountInString(textValue) <= maximum {
		return textValue
	}
	characters := []rune(textValue)
	return string(characters[:maximum])
}
