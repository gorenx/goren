package sessionapi

import (
	"context"
	"errors"
	"unicode/utf8"

	rpc "github.com/gorenx/goren/apiproxy/rpc"
	sessionframes "github.com/gorenx/goren/apiproxy/session/events"
	"github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/session"
	sessionquery "github.com/gorenx/goren/session/query"
)

const (
	sessionSearchResultLimit       = 20
	sessionSearchSnippetMaximum    = 240
	sessionSearchProviderCallLimit = 1_000
)

// Search supplies ranked Session matches without exposing index lifecycle.
type Search interface {
	SearchSessions(context.Context, sessionquery.SearchSessionsRequest) (sessionquery.SessionSearchPage, error)
}

// Visibility is the authorization boundary consumed by Session
// Search. It deliberately exposes identities instead of session.list DTOs.
type Visibility interface {
	VisibleSessionIDs(context.Context) (map[session.SessionID]struct{}, error)
}

// SearchGateway owns the session.search use case: fixed searchable
// event scope, list-equivalent visibility, cursor recovery, and wire mapping.
// Query observation and full-text mechanics remain in session/query.
type SearchGateway struct {
	queries    Search
	visibility Visibility
}

// NewSearchGateway binds Session Query to the API authorization view.
func NewSearchGateway(
	queries Search,
	visibilitySource Visibility,
) (*SearchGateway, error) {
	if queries == nil || visibilitySource == nil {
		return nil, errors.New("apiproxy: Session Search Gateway dependencies are incomplete")
	}
	return &SearchGateway{
		queries:    queries,
		visibility: visibilitySource,
	}, nil
}

// Search enforces list-equivalent visibility and projects the fixed current
// user/assistant message surface into the source wire result.
func (owner *SearchGateway) Search(
	requestContext context.Context,
	call rpc.Request[SearchRequest],
) (rpc.Outcome[SearchValue], error) {
	cancelled := func() rpc.Outcome[SearchValue] {
		return rpc.Fail[SearchValue](rpc.NewRPCError(
			connection.ErrorCancelled, "session search was aborted", struct{}{},
		))
	}
	if requestContext.Err() != nil {
		return cancelled(), nil
	}
	visibleIDs, err := owner.visibility.VisibleSessionIDs(requestContext)
	if err != nil {
		return rpc.Outcome[SearchValue]{}, err
	}
	if len(visibleIDs) == 0 {
		return rpc.OK(SearchValue{
			Items: []SearchItem{},
		}), nil
	}
	authorized := make([]SearchItem, 0, sessionSearchResultLimit+1)
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
			return rpc.Outcome[SearchValue]{}, errors.New("apiproxy: Session search exceeded provider call budget")
		}
		page, searchErr := owner.queries.SearchSessions(requestContext, sessionquery.SearchSessionsRequest{
			Text: call.Payload.Query,
			Events: sessionquery.EventConstraints{
				Types:    []string{session.UserMessageEventName, session.AssistantMessageEventName},
				Surfaces: []sessionquery.Surface{sessionquery.SurfaceCurrent},
			},
			Limit:  sessionSearchResultLimit,
			Cursor: cursor,
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
			return rpc.Fail[SearchValue](rpc.NewRPCError(
				connection.ErrorInternal, "session search failed: "+searchErr.Error(), struct{}{},
			)), nil
		}
		if len(page.Items) > sessionSearchResultLimit {
			return rpc.Outcome[SearchValue]{}, errors.New("apiproxy: Session Query returned too many search hits")
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
			authorized = append(authorized, SearchItem{
				SessionID: sessionframes.SessionID(identifier),
				Snippet:   truncateCodePoints(hit.BestMatch.Snippet, sessionSearchSnippetMaximum),
			})
		}
		if len(authorized) > sessionSearchResultLimit || page.NextCursor == "" {
			break
		}
		if _, repeated := seenCursors[page.NextCursor]; repeated {
			return rpc.Outcome[SearchValue]{}, errors.New("apiproxy: Session Query repeated a continuation cursor")
		}
		seenCursors[page.NextCursor] = struct{}{}
		cursor = page.NextCursor
	}
	hasMore := len(authorized) > sessionSearchResultLimit
	if hasMore {
		authorized = authorized[:sessionSearchResultLimit]
	}
	if authorized == nil {
		authorized = []SearchItem{}
	}
	return rpc.OK(SearchValue{
		Items:   authorized,
		HasMore: hasMore,
	}), nil
}

func truncateCodePoints(textValue string, maximum int) string {
	if utf8.RuneCountInString(textValue) <= maximum {
		return textValue
	}
	characters := []rune(textValue)
	return string(characters[:maximum])
}
