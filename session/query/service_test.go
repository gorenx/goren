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

type queryPostCommitFailureSink struct{}

func (queryPostCommitFailureSink) ReportPostCommitFailure(session.PostCommitFailure) {}

type queryPersistencePlugin struct {
	plugin.Base
	logs      map[session.SessionID]sesspersist.Inspection
	revisions map[session.SessionID]sesspersist.Revision
}

func (*queryPersistencePlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "fixture-query-persistence",
		Provides: []plugin.ServiceType{
			plugin.ServiceOf[sesspersist.Persistence](),
		},
	}
}

func (*queryPersistencePlugin) Apply(context.Context) error {
	return nil
}

func (*queryPersistencePlugin) Dispose(context.Context) error {
	return nil
}

func (*queryPersistencePlugin) Locate(session.Header) (sesspersist.Location, bool) {
	return sesspersist.Location{}, false
}

func (*queryPersistencePlugin) SupportsRawArtifacts() bool {
	return false
}

func (*queryPersistencePlugin) ReadRaw(
	context.Context,
	session.SessionID,
) (sesspersist.RawArtifact, bool, error) {
	return sesspersist.RawArtifact{}, false, errors.New("raw artifacts unavailable")
}

func (*queryPersistencePlugin) Create(context.Context, session.Header) error {
	return errors.New("fixture does not create durable Sessions")
}

func (*queryPersistencePlugin) Append(
	context.Context,
	session.SessionID,
	[]session.Event,
) error {
	return errors.New("fixture does not append durable Sessions")
}

func (*queryPersistencePlugin) Prepare(
	context.Context,
	session.SessionID,
) (*session.Preparation, error) {
	return nil, errors.New("fixture does not prepare durable Sessions")
}

func (storage *queryPersistencePlugin) Load(
	requestContext context.Context,
	identifier session.SessionID,
) (sesspersist.Inspection, error) {
	return storage.Inspect(requestContext, identifier)
}

func (storage *queryPersistencePlugin) Inspect(
	_ context.Context,
	identifier session.SessionID,
) (sesspersist.Inspection, error) {
	loaded, found := storage.logs[identifier]
	if !found {
		return sesspersist.Inspection{}, &sesspersist.NotFoundError{
			ID: identifier,
		}
	}
	return loaded, nil
}

func (*queryPersistencePlugin) ReadFrom(
	context.Context,
	session.SessionID,
	int64,
) (sesspersist.Inspection, error) {
	return sesspersist.Inspection{}, errors.New("fixture does not read suffixes")
}

func (storage *queryPersistencePlugin) List(context.Context) ([]session.Header, error) {
	result := make([]session.Header, 0, len(storage.logs))
	for _, loaded := range storage.logs {
		result = append(result, loaded.Header)
	}
	return result, nil
}

func (storage *queryPersistencePlugin) ListSnapshots(
	context.Context,
) ([]sesspersist.Snapshot, error) {
	result := make([]sesspersist.Snapshot, 0, len(storage.logs))
	for identifier, loaded := range storage.logs {
		result = append(
			result,
			sesspersist.Snapshot{
				Header:   loaded.Header,
				Revision: storage.revisions[identifier],
			},
		)
	}
	return result, nil
}

type queryIndexOpener struct {
	settings querysqlite.Config
}

func (opener queryIndexOpener) OpenIndex(
	requestContext context.Context,
) (sessionquery.Index, error) {
	return querysqlite.Open(requestContext, opener.settings)
}

type queryFixture struct {
	persistence *queryPersistencePlugin
	store       *session.MemoryStore
	service     *sessionquery.Service
}

func newQueryFixture(
	testingContext *testing.T,
	persistence *queryPersistencePlugin,
) *queryFixture {
	testingContext.Helper()
	store, err := session.NewMemoryStore(
		session.MemoryStoreOptions{
			PostCommitFailures: queryPostCommitFailureSink{},
		},
	)
	if err != nil {
		testingContext.Fatal(err)
	}
	queryService, err := sessionquery.New(
		queryIndexOpener{
			settings: querysqlite.Config{
				Path: filepath.Join(testingContext.TempDir(), "query.sqlite"),
			},
		},
		sessionquery.Config{},
	)
	if err != nil {
		testingContext.Fatal(err)
	}
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	if _, err := runtimeEngine.Start(
		context.Background(),
		queryService,
		persistence,
		store,
	); err != nil {
		testingContext.Fatal(err)
	}
	testingContext.Cleanup(func() {
		if err := runtimeEngine.Shutdown(context.Background()); err != nil {
			testingContext.Error(err)
		}
	})
	return &queryFixture{
		persistence: persistence,
		store:       store,
		service:     queryService,
	}
}

func TestServiceUsesLiveLogAndTracesSurfaceAndLineage(t *testing.T) {
	t.Parallel()
	requestContext := context.Background()
	createdAt := int64(100)
	parentID := session.SessionID("parent")
	childID := session.SessionID("child")
	parent, err := session.New(
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
	storage := &queryPersistencePlugin{
		logs: map[session.SessionID]sesspersist.Inspection{
			parentID: {
				Header: parent.Header(),
				Events: parent.Events(),
			},
		},
		revisions: map[session.SessionID]sesspersist.Revision{
			parentID: "parent-r1",
		},
	}
	fixture := newQueryFixture(t, storage)
	handle, err := fixture.store.Create(
		requestContext,
		&childID,
		session.CreateOptions{
			Metadata: session.Metadata{
				CreatedAt:     &createdAt,
				ParentSession: &parentID,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	child := handle.Session()
	appendUser(t, child, "live question", session.SurfaceAppend(), nil)
	appendAssistant(t, child, "obsolete answer", session.SurfaceAppend())
	sources := []int64{0, 1}
	appendUser(t, child, "replacement summary", session.SurfaceReplace(0, 1), &sources)
	storage.logs[childID] = sesspersist.Inspection{
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
	storage.revisions[childID] = "child-cold-r1"

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
	storage := &queryPersistencePlugin{
		logs:      map[session.SessionID]sesspersist.Inspection{},
		revisions: map[session.SessionID]sesspersist.Revision{},
	}
	fixture := newQueryFixture(t, storage)
	first := createLiveSession(t, fixture, "first", 100)
	second := createLiveSession(t, fixture, "second", 200)
	appendUser(t, first, "alpha literal result", session.SurfaceAppend(), nil)
	appendUser(t, second, "alpha another result", session.SurfaceAppend(), nil)

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
			SessionID: first.ID(),
			Text:      "changed",
			Limit:     5,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(events.Items) != 1 || events.Items[0].SessionID != first.ID() ||
		events.Items[0].Seq != 1 {
		t.Fatalf("within-Session search = %#v", events)
	}
}

func createLiveSession(
	testingContext *testing.T,
	fixture *queryFixture,
	identifier session.SessionID,
	createdAt int64,
) *session.Session {
	testingContext.Helper()
	handle, err := fixture.store.Create(
		context.Background(),
		&identifier,
		session.CreateOptions{
			Metadata: session.Metadata{
				CreatedAt: &createdAt,
			},
		},
	)
	if err != nil {
		testingContext.Fatal(err)
	}
	return handle.Session()
}

func appendUser(
	testingContext *testing.T,
	conversation *session.Session,
	textValue string,
	operation session.SurfaceOperation,
	sources *[]int64,
) {
	testingContext.Helper()
	messageValue, err := llm.NewUserMessage(
		llm.UserMessageInput{
			Content: []llm.ContentBlock{
				llm.NewTextBlock(textValue),
			},
			Source: llm.UserMessageSource{
				Kind: "user",
			},
		},
	)
	if err != nil {
		testingContext.Fatal(err)
	}
	if _, err := session.AppendSurface(
		conversation,
		session.UserMessageAdded,
		messageValue,
		session.SurfaceIntent{
			Operation:       operation,
			SourceEventSeqs: sources,
		},
	); err != nil {
		testingContext.Fatal(err)
	}
}

func appendAssistant(
	testingContext *testing.T,
	conversation *session.Session,
	textValue string,
	operation session.SurfaceOperation,
) {
	testingContext.Helper()
	messageValue, err := llm.NewAssistantMessage(
		llm.AssistantMessageInput{
			Content: []llm.ContentBlock{
				llm.NewTextBlock(textValue),
			},
			Source: llm.ModelMessageSource{
				Provider: "test",
				Model:    "test",
			},
		},
	)
	if err != nil {
		testingContext.Fatal(err)
	}
	if _, err := session.AppendSurface(
		conversation,
		session.AssistantMessaged,
		session.AssistantMessage{
			Turn:    1,
			Step:    1,
			Message: messageValue,
		},
		session.SurfaceIntent{
			Operation: operation,
		},
	); err != nil {
		testingContext.Fatal(err)
	}
}
