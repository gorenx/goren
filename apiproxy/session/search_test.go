package sessionapi

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	api "github.com/gorenx/goren/apiproxy"
	"github.com/gorenx/goren/session"
	sessionquery "github.com/gorenx/goren/session/query"
)

type searchQueryStub struct {
	sessionquery.QueryService
	requests []sessionquery.SearchSessionsRequest
	pages    []sessionquery.SessionSearchPage
}

func (stub *searchQueryStub) SearchSessions(
	_ context.Context,
	criteria sessionquery.SearchSessionsRequest,
) (sessionquery.SessionSearchPage, error) {
	stub.requests = append(stub.requests, criteria)
	position := len(stub.requests) - 1
	if position >= len(stub.pages) {
		return sessionquery.SessionSearchPage{}, nil
	}
	return stub.pages[position], nil
}

type visibilityStub map[session.SessionID]struct{}

func (visible visibilityStub) VisibleSessionIDs(context.Context) (map[session.SessionID]struct{}, error) {
	return visible, nil
}

func TestSearchGatewayAppliesFixedScopeAndVisibility(t *testing.T) {
	t.Parallel()
	queryStub := &searchQueryStub{pages: []sessionquery.SessionSearchPage{{
		Items: []sessionquery.SessionHit{
			searchHit("hidden", session.UserMessageEventName, sessionquery.SurfaceCurrent, "hidden"),
			searchHit("visible", session.AssistantMessageEventName, sessionquery.SurfaceCurrent, strings.Repeat("界", 241)),
			searchHit("shadowed", session.UserMessageEventName, sessionquery.SurfaceShadowed, "old"),
		},
	}}}
	searchService, err := NewSearchGateway(queryStub, visibilityStub{
		"visible": {}, "shadowed": {},
	})
	if err != nil {
		t.Fatal(err)
	}
	methods := api.NewCatalog()
	if err := api.RegisterUnary(methods, api.SessionSearchMethod, api.DecodeSessionSearchRequest, searchService.Search); err != nil {
		t.Fatal(err)
	}
	result, err := methods.DispatchUnary(
		context.Background(), api.SessionSearchMethod, "search-rpc", json.RawMessage(`{"query":"needle"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	var value api.SessionSearchValue
	if !result.OK || result.Error != nil || json.Unmarshal(result.Value, &value) != nil ||
		len(value.Items) != 1 || value.Items[0].SessionID != "visible" ||
		len([]rune(value.Items[0].Snippet)) != sessionSearchSnippetMaximum {
		t.Fatalf("search result = %#v, value = %#v", result, value)
	}
	if len(queryStub.requests) != 1 {
		t.Fatalf("query requests = %#v", queryStub.requests)
	}
	criteria := queryStub.requests[0]
	if criteria.Text != "needle" || criteria.Limit != sessionSearchResultLimit ||
		len(criteria.Events.Types) != 2 || criteria.Events.Types[0] != session.UserMessageEventName ||
		criteria.Events.Types[1] != session.AssistantMessageEventName ||
		len(criteria.Events.Surfaces) != 1 || criteria.Events.Surfaces[0] != sessionquery.SurfaceCurrent {
		t.Fatalf("fixed Query scope = %#v", criteria)
	}
}

func searchHit(
	identifier session.SessionID,
	eventType string,
	surface sessionquery.Surface,
	snippet string,
) sessionquery.SessionHit {
	return sessionquery.SessionHit{
		SessionRecord: sessionquery.SessionRecord{Header: session.Header{ID: identifier}},
		BestMatch: sessionquery.EventHit{
			SessionID: identifier, Type: eventType, Surface: surface, Snippet: snippet,
		},
	}
}
