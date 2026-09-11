package query_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	sessionquery "github.com/gorenx/goren/session/query"
	querysqlite "github.com/gorenx/goren/session/query/sqlite"
)

type storedSources struct {
	logs map[session.SessionID]sessionquery.LogSnapshot
}

type queryFixture struct {
	persistence *storedSources
	live        map[session.SessionID]*session.Log
	service     *sessionquery.Service
}

func newQueryFixture(t *testing.T, stored *storedSources) *queryFixture {
	t.Helper()
	backend, err := querysqlite.Open(context.Background(), querysqlite.Config{
		Path: filepath.Join(t.TempDir(), "query.sqlite"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := backend.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	owner, err := sessionquery.New(backend, sessionquery.Config{})
	if err != nil {
		t.Fatal(err)
	}
	return &queryFixture{
		persistence: stored,
		live:        make(map[session.SessionID]*session.Log),
		service:     owner,
	}
}

func (fixture *queryFixture) observe(t *testing.T) {
	t.Helper()
	sources := make([]sessionquery.Source, 0)
	for _, snapshot := range fixture.persistence.logs {
		sources = append(sources, sessionquery.Source{
			Log:       snapshot,
			Persisted: true,
		})
	}
	for _, conversation := range fixture.live {
		sources = append(sources, sessionquery.Source{
			Log: sessionquery.LogSnapshot{
				Header: conversation.Header(),
				Events: conversation.Events(),
			},
			Live: true,
		})
	}
	if err := fixture.service.Observe(sources); err != nil {
		t.Fatal(err)
	}
}

func TestServiceUsesLiveLogAndTracesSurfaceAndLineage(t *testing.T) {
	t.Parallel()
	requestContext := context.Background()
	createdAt := int64(100)
	parentID := session.SessionID("parent")
	childID := session.SessionID("child")
	parent, err := session.NewLog(
		parentID,
		session.CreateOptions{
			Metadata: session.Metadata{
				CreatedAt: &createdAt,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	storage := &storedSources{
		logs: map[session.SessionID]sessionquery.LogSnapshot{
			parentID: {
				Header: parent.Header(),
				Events: parent.Events(),
			},
		},
	}
	fixture := newQueryFixture(t, storage)
	child, err := session.NewLog(childID, session.CreateOptions{
		Metadata: session.Metadata{
			CreatedAt:     &createdAt,
			ParentSession: &parentID,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.live[childID] = child
	appendUser(t, child, "live question", session.SurfaceAppend(), nil)
	appendAssistant(t, child, "obsolete answer", session.SurfaceAppend())
	sources := []int64{0, 1}
	appendUser(t, child, "replacement summary", session.SurfaceReplace(0, 1), &sources)
	storage.logs[childID] = sessionquery.LogSnapshot{
		Header: child.Header(),
		Events: []session.Event{
			{
				Type: session.TurnStartEventName,
				Seq:  0,
				Time: 1,
				Data: []byte(`{"turn":1}`),
			},
		},
	}

	fixture.observe(t)
	listed, err := fixture.service.ListSessions(requestContext)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 || !listed[0].Live || !listed[0].Persisted ||
		listed[0].Header.ID != childID {
		t.Fatalf("logical Session listing = %#v", listed)
	}
	loaded, err := fixture.service.ReadSession(requestContext, childID)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Events) != 3 || string(loaded.Events[0].Data) == `{"turn":1}` {
		t.Fatalf("live-preferred log = %#v", loaded.Events)
	}
	surface, err := fixture.service.ReadSurface(requestContext, childID)
	if err != nil {
		t.Fatal(err)
	}
	if len(surface.Events) != 1 || surface.Events[0].Seq != 2 ||
		surface.CapturedThroughSeq == nil || *surface.CapturedThroughSeq != 2 {
		t.Fatalf("surface = %#v", surface)
	}
	events, err := fixture.service.ListEvents(requestContext, childID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || events[0].Surface != sessionquery.SurfaceShadowed ||
		events[1].Surface != sessionquery.SurfaceShadowed ||
		events[2].Surface != sessionquery.SurfaceCurrent {
		t.Fatalf("surface classifications = %#v", events)
	}
	trace, err := fixture.service.TraceEvent(
		requestContext,
		sessionquery.EventTraceRequest{
			SessionID: childID,
			Seq:       0,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if trace.ReplacedBy == nil || *trace.ReplacedBy != 2 ||
		len(trace.ReplacementChain) != 1 || trace.ReplacementChain[0] != 2 {
		t.Fatalf("event trace = %#v", trace)
	}
	lineage, err := fixture.service.TraceSession(requestContext, childID)
	if err != nil {
		t.Fatal(err)
	}
	if !lineage.Complete || lineage.Root == nil || lineage.Root.Header.ID != parentID ||
		len(lineage.Ancestors) != 1 || lineage.Ancestors[0].Header.ID != parentID {
		t.Fatalf("lineage = %#v", lineage)
	}
}

func TestServiceSearchesLiteralTextAndRejectsStaleCursor(t *testing.T) {
	t.Parallel()
	requestContext := context.Background()
	storage := &storedSources{
		logs: map[session.SessionID]sessionquery.LogSnapshot{},
	}
	fixture := newQueryFixture(t, storage)
	first := createLiveSession(t, fixture, "first", 100)
	second := createLiveSession(t, fixture, "second", 200)
	appendUser(t, first, "alpha literal result", session.SurfaceAppend(), nil)
	appendUser(t, second, "alpha another result", session.SurfaceAppend(), nil)

	fixture.observe(t)
	page, err := fixture.service.SearchSessions(
		requestContext,
		sessionquery.SearchSessionsRequest{
			Text:  "alpha",
			Limit: 1,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.NextCursor == "" || page.Items[0].BestMatch.Snippet == "" {
		t.Fatalf("first search page = %#v", page)
	}
	appendAssistant(t, first, "alpha changed generation", session.SurfaceAppend())
	fixture.observe(t)
	_, err = fixture.service.SearchSessions(
		requestContext,
		sessionquery.SearchSessionsRequest{
			Text:   "alpha",
			Limit:  1,
			Cursor: page.NextCursor,
		},
	)
	var classified *sessionquery.Error
	if !errors.As(err, &classified) || classified.Code != sessionquery.ErrorStaleCursor {
		t.Fatalf("stale cursor error = %T %v", err, err)
	}
	events, err := fixture.service.SearchEvents(
		requestContext,
		sessionquery.SearchEventsRequest{
			SessionID: first.Header().ID,
			Text:      "changed",
			Limit:     5,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(events.Items) != 1 || events.Items[0].SessionID != first.Header().ID ||
		events.Items[0].Seq != 1 {
		t.Fatalf("within-Session search = %#v", events)
	}
}

func createLiveSession(t *testing.T, fixture *queryFixture, identifier session.SessionID, createdAt int64) *session.Log {
	t.Helper()
	conversation, err := session.NewLog(identifier, session.CreateOptions{
		Metadata: session.Metadata{CreatedAt: &createdAt},
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.live[identifier] = conversation
	return conversation
}

func appendUser(
	testingContext *testing.T,
	conversation *session.Log,
	textValue string,
	operation session.SurfaceOperation,
	sources *[]int64,
) {
	testingContext.Helper()
	messageValue, err := agentmessage.NewUserMessage(
		agentmessage.UserMessageInput{
			Content: []agentmessage.ContentBlock{
				agentmessage.NewTextBlock(textValue),
			},
			Source: agentmessage.UserMessageSource{
				Kind: "user",
			},
		},
	)
	if err != nil {
		testingContext.Fatal(err)
	}
	{
		draft, err := session.NewSurfaceEventDraft(session.UserMessageAdded,
			messageValue,
			session.SurfaceIntent{
				Operation:       operation,
				SourceEventSeqs: sources,
			})
		if err == nil {
			_, err = conversation.Commit(context.Background(), session.Batch(draft))
		}
		if err != nil {
			testingContext.Fatal(err)
		}
	}
}

func appendAssistant(
	testingContext *testing.T,
	conversation *session.Log,
	textValue string,
	operation session.SurfaceOperation,
) {
	testingContext.Helper()
	messageValue, err := agentmessage.NewAssistantMessage(
		agentmessage.AssistantMessageInput{
			Content: []agentmessage.ContentBlock{
				agentmessage.NewTextBlock(textValue),
			},
			Source: agentmessage.ModelMessageSource{
				Provider: "test",
				Model:    "test",
			},
		},
	)
	if err != nil {
		testingContext.Fatal(err)
	}
	{
		draft, err := session.NewSurfaceEventDraft(session.AssistantMessaged,
			session.AssistantMessage{
				Turn:    1,
				Step:    1,
				Message: messageValue,
			},
			session.SurfaceIntent{
				Operation: operation,
			})
		if err == nil {
			_, err = conversation.Commit(context.Background(), session.Batch(draft))
		}
		if err != nil {
			testingContext.Fatal(err)
		}
	}
}

func TestObservationIsAtomicDetachedAndContentStable(t *testing.T) {
	fixture := newQueryFixture(t, &storedSources{})
	conversation := createLiveSession(t, fixture, "observed", 100)
	appendUser(t, conversation, "alpha unchanged", session.SurfaceAppend(), nil)
	original := sessionquery.Source{
		Log: sessionquery.LogSnapshot{
			Header: conversation.Header(),
			Events: conversation.Events(),
		},
		Live: true,
	}
	if err := fixture.service.Observe([]sessionquery.Source{original}); err != nil {
		t.Fatal(err)
	}
	original.Log.Events[0].Data[0] = '!'
	loaded, err := fixture.service.ReadSession(context.Background(), "observed")
	if err != nil || loaded.Events[0].Data[0] == '!' {
		t.Fatal("observation retained caller memory")
	}
	loaded.Events[0].Data[0] = '?'
	loaded, err = fixture.service.ReadSession(context.Background(), "observed")
	if err != nil || loaded.Events[0].Data[0] == '?' {
		t.Fatal("read exposed owner memory")
	}
	bad := sessionquery.Source{
		Log: sessionquery.LogSnapshot{
			Header: conversation.Header(),
			Events: []session.Event{{
				Type: session.UserMessageEventName,
				Seq:  4,
				Data: []byte(`{}`),
			}},
		},
		Live: true,
	}
	if err := fixture.service.Observe([]sessionquery.Source{bad}); err == nil {
		t.Fatal("invalid observation accepted")
	}
	loaded, err = fixture.service.ReadSession(context.Background(), "observed")
	if err != nil || len(loaded.Events) != 1 {
		t.Fatal("failed observation replaced valid corpus")
	}
	// An unusable cold log must not override a valid live source.
	bad.Live = false
	bad.Persisted = true
	live := sessionquery.Source{
		Log: sessionquery.LogSnapshot{
			Header: conversation.Header(),
			Events: conversation.Events(),
		},
		Live: true,
	}
	if err := fixture.service.Observe([]sessionquery.Source{bad, live}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.Observe([]sessionquery.Source{live, bad}); err != nil {
		t.Fatal(err)
	}
	bad.Log.Header.CreatedAt++
	if err := fixture.service.Observe([]sessionquery.Source{live, bad}); err == nil {
		t.Fatal("conflicting source headers accepted")
	}
}

func TestUnchangedObservationPreservesSearchCursor(t *testing.T) {
	fixture := newQueryFixture(t, &storedSources{})
	first := createLiveSession(t, fixture, "first", 100)
	second := createLiveSession(t, fixture, "second", 200)
	appendUser(t, first, "alpha first", session.SurfaceAppend(), nil)
	appendUser(t, second, "alpha second", session.SurfaceAppend(), nil)
	fixture.observe(t)
	page, err := fixture.service.SearchSessions(context.Background(), sessionquery.SearchSessionsRequest{
		Text:  "alpha",
		Limit: 1,
	})
	if err != nil || page.NextCursor == "" {
		t.Fatalf("page = %#v, %v", page, err)
	}
	fixture.observe(t)
	next, err := fixture.service.SearchSessions(context.Background(), sessionquery.SearchSessionsRequest{
		Text:   "alpha",
		Limit:  1,
		Cursor: page.NextCursor,
	})
	if err != nil || len(next.Items) != 1 {
		t.Fatalf("unchanged observation invalidated cursor: %#v, %v", next, err)
	}
}
