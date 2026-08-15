package query_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	sesspersist "github.com/gorenx/goren/session/persistence"
	sessionquery "github.com/gorenx/goren/session/query"
	querysqlite "github.com/gorenx/goren/session/query/sqlite"
)

type queryPersistence struct {
	sesspersist.Persistence
	logs      map[session.SessionID]sesspersist.Inspection
	revisions map[session.SessionID]sesspersist.Revision
}

func (storage *queryPersistence) ListSnapshots(context.Context) ([]sesspersist.Snapshot, error) {
	result := make([]sesspersist.Snapshot, 0, len(storage.logs))
	for identifier, loaded := range storage.logs {
		result = append(result, sesspersist.Snapshot{
			Header: loaded.Header, Revision: storage.revisions[identifier],
		})
	}
	return result, nil
}

func (storage *queryPersistence) Inspect(_ context.Context, identifier session.SessionID) (sesspersist.Inspection, error) {
	loaded, found := storage.logs[identifier]
	if !found {
		return sesspersist.Inspection{}, &sesspersist.NotFoundError{ID: identifier}
	}
	return loaded, nil
}

type queryFixture struct {
	path        string
	persistence *queryPersistence
	scope       *plugin.Scope
	store       *session.MemoryStore
	service     *sessionquery.Service
}

func (fixture *queryFixture) Manifest() plugin.Manifest {
	return plugin.Manifest{Name: "session-query-fixture"}
}

func (fixture *queryFixture) Apply(requestContext context.Context, pluginScope *plugin.Scope) error {
	storeProvider, err := session.NewMemoryStore(pluginScope, session.MemoryStoreOptions{})
	if err != nil {
		return err
	}
	if err := pluginScope.Effect(requestContext, "sessions", func(context.Context) (plugin.Disposer, error) {
		return storeProvider.Close, nil
	}); err != nil {
		return err
	}
	derivedIndex, err := querysqlite.Open(requestContext, querysqlite.Config{Path: fixture.path})
	if err != nil {
		return err
	}
	queries, err := sessionquery.New(
		pluginScope, storeProvider, fixture.persistence, derivedIndex, sessionquery.Config{},
	)
	if err != nil {
		_ = derivedIndex.Close(requestContext)
		return err
	}
	fixture.scope = pluginScope
	fixture.store = storeProvider
	fixture.service = queries
	return nil
}

func TestServiceUsesLiveLogAndTracesSurfaceAndLineage(t *testing.T) {
	t.Parallel()
	requestContext := context.Background()
	createdAt := int64(100)
	parentID := session.SessionID("parent")
	childID := session.SessionID("child")
	parent, err := session.New(parentID, session.CreateOptions{Metadata: session.Metadata{CreatedAt: &createdAt}})
	if err != nil {
		t.Fatal(err)
	}
	storage := &queryPersistence{
		logs: map[session.SessionID]sesspersist.Inspection{
			parentID: {Header: parent.Header(), Events: parent.Events()},
		},
		revisions: map[session.SessionID]sesspersist.Revision{parentID: "parent-r1"},
	}
	engine := plugin.NewRuntime()
	fixture := &queryFixture{path: filepath.Join(t.TempDir(), "query.sqlite"), persistence: storage}
	handle, err := engine.Load(requestContext, fixture)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Unload(requestContext, handle) })

	child, err := fixture.store.Create(requestContext, fixture.scope, &childID, session.CreateOptions{
		Metadata: session.Metadata{CreatedAt: &createdAt, ParentSession: &parentID},
	})
	if err != nil {
		t.Fatal(err)
	}
	appendUser(t, child, "live question", session.SurfaceAppend(), nil)
	appendAssistant(t, child, "obsolete answer", session.SurfaceAppend())
	sources := []int64{0, 1}
	appendUser(t, child, "replacement summary", session.SurfaceReplace(0, 1), &sources)
	storage.logs[childID] = sesspersist.Inspection{
		Header: child.Header(),
		Events: []session.Event{{Type: session.TurnStartEventName, Seq: 0, Time: 1, Data: []byte(`{"turn":1}`)}},
	}
	storage.revisions[childID] = "child-cold-r1"

	listed, err := fixture.service.ListSessions(requestContext)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 || !listed[0].Live || !listed[0].Persisted || listed[0].Header.ID != childID {
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
	if len(surface.Events) != 1 || surface.Events[0].Seq != 2 || surface.CapturedThroughSeq == nil || *surface.CapturedThroughSeq != 2 {
		t.Fatalf("surface = %#v", surface)
	}
	events, err := fixture.service.ListEvents(requestContext, childID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || events[0].Surface != sessionquery.SurfaceShadowed ||
		events[1].Surface != sessionquery.SurfaceShadowed || events[2].Surface != sessionquery.SurfaceCurrent {
		t.Fatalf("surface classifications = %#v", events)
	}
	trace, err := fixture.service.TraceEvent(requestContext, sessionquery.EventTraceRequest{SessionID: childID, Seq: 0})
	if err != nil {
		t.Fatal(err)
	}
	if trace.ReplacedBy == nil || *trace.ReplacedBy != 2 || len(trace.ReplacementChain) != 1 || trace.ReplacementChain[0] != 2 {
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
	engine := plugin.NewRuntime()
	fixture := &queryFixture{
		path: filepath.Join(t.TempDir(), "query.sqlite"),
		persistence: &queryPersistence{
			logs: map[session.SessionID]sesspersist.Inspection{}, revisions: map[session.SessionID]sesspersist.Revision{},
		},
	}
	handle, err := engine.Load(requestContext, fixture)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Unload(requestContext, handle) })
	first := createLiveSession(t, fixture, "first", 100)
	second := createLiveSession(t, fixture, "second", 200)
	appendUser(t, first, "alpha literal result", session.SurfaceAppend(), nil)
	appendUser(t, second, "alpha another result", session.SurfaceAppend(), nil)

	page, err := fixture.service.SearchSessions(requestContext, sessionquery.SearchSessionsRequest{
		Text: "alpha", Limit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.NextCursor == "" || page.Items[0].BestMatch.Snippet == "" {
		t.Fatalf("first search page = %#v", page)
	}
	appendAssistant(t, first, "alpha changed generation", session.SurfaceAppend())
	_, err = fixture.service.SearchSessions(requestContext, sessionquery.SearchSessionsRequest{
		Text: "alpha", Limit: 1, Cursor: page.NextCursor,
	})
	var classified *sessionquery.Error
	if !errors.As(err, &classified) || classified.Code != sessionquery.ErrorStaleCursor {
		t.Fatalf("stale cursor error = %T %v", err, err)
	}
	events, err := fixture.service.SearchEvents(requestContext, sessionquery.SearchEventsRequest{
		SessionID: first.ID(), Text: "changed", Limit: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events.Items) != 1 || events.Items[0].SessionID != first.ID() || events.Items[0].Seq != 1 {
		t.Fatalf("within-Session search = %#v", events)
	}
}

func createLiveSession(t *testing.T, fixture *queryFixture, identifier session.SessionID, createdAt int64) *session.Session {
	t.Helper()
	conversation, err := fixture.store.Create(context.Background(), fixture.scope, &identifier, session.CreateOptions{
		Metadata: session.Metadata{CreatedAt: &createdAt},
	})
	if err != nil {
		t.Fatal(err)
	}
	return conversation
}

func appendUser(
	t *testing.T,
	conversation *session.Session,
	textValue string,
	operation session.SurfaceOperation,
	sources *[]int64,
) {
	t.Helper()
	messageValue, err := llm.NewUserMessage(llm.UserMessageInput{
		Content: []llm.ContentBlock{llm.NewTextBlock(textValue)},
		Source:  llm.UserMessageSource{Kind: "user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.AppendSurface(conversation, session.UserMessageAdded, messageValue, session.SurfaceIntent{
		Operation: operation, SourceEventSeqs: sources,
	}); err != nil {
		t.Fatal(err)
	}
}

func appendAssistant(t *testing.T, conversation *session.Session, textValue string, operation session.SurfaceOperation) {
	t.Helper()
	messageValue, err := llm.NewAssistantMessage(llm.AssistantMessageInput{
		Content: []llm.ContentBlock{llm.NewTextBlock(textValue)},
		Source:  llm.ModelMessageSource{Provider: "test", Model: "test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.AppendSurface(
		conversation,
		session.AssistantMessaged,
		session.AssistantMessage{Turn: 1, Step: 1, Message: messageValue},
		session.SurfaceIntent{Operation: operation},
	); err != nil {
		t.Fatal(err)
	}
}
