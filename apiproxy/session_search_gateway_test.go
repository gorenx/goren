package apiproxy

import (
	"context"
	"strings"
	"testing"

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

func TestSessionSearchGatewayAppliesFixedScopeAndVisibility(t *testing.T) {
	t.Parallel()
	queryStub := &searchQueryStub{pages: []sessionquery.SessionSearchPage{{
		Items: []sessionquery.SessionHit{
			searchHit("hidden", session.UserMessageEventName, sessionquery.SurfaceCurrent, "hidden"),
			searchHit("visible", session.AssistantMessageEventName, sessionquery.SurfaceCurrent, strings.Repeat("界", 241)),
			searchHit("shadowed", session.UserMessageEventName, sessionquery.SurfaceShadowed, "old"),
		},
	}}}
	gateway, err := NewSessionSearchGateway(queryStub, visibilityStub{
		"visible": {}, "shadowed": {},
	})
	if err != nil {
		t.Fatal(err)
	}
	searchResult, err := gateway.Search(context.Background(), Request[SessionSearchRequest]{
		Payload: SessionSearchRequest{Query: "needle"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if searchResult.rpcError != nil || len(searchResult.value.Items) != 1 || searchResult.value.Items[0].SessionID != "visible" ||
		len([]rune(searchResult.value.Items[0].Snippet)) != sessionSearchSnippetMaximum {
		t.Fatalf("search outcome = %#v", searchResult)
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

func TestDecodeSessionSearchRequestUsesJavaScriptStringLength(t *testing.T) {
	t.Parallel()
	accepted, issues := DecodeSessionSearchRequest([]byte(`{"query":"  exact phrase  "}`))
	if len(issues) != 0 || accepted.Query != "exact phrase" {
		t.Fatalf("accepted query = %#v, issues = %#v", accepted, issues)
	}
	for _, input := range []string{
		`{"query":"   "}`,
		`{"query":"bad\u0000query"}`,
		`{"query":"` + strings.Repeat("😀", 251) + `"}`,
	} {
		if _, issues := DecodeSessionSearchRequest([]byte(input)); len(issues) == 0 {
			t.Fatalf("invalid query accepted: %q", input)
		}
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
