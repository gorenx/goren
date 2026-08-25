package persistence_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	sesspersist "github.com/gorenx/goren/session/persistence"
	sesssqlite "github.com/gorenx/goren/session/persistence/sqlite"
)

type persistenceFailureReporter struct{}

type sessionStoreProbe struct {
	plugin.Base
	store session.LiveStore
}

func (*sessionStoreProbe) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "session-persistence-store-probe",
		Requires: []plugin.ServiceType{
			plugin.ServiceOf[session.LiveStore](),
		},
	}
}

func (probe *sessionStoreProbe) Apply(context.Context) error {
	liveStore, err := plugin.Require[session.LiveStore](probe)
	if err != nil {
		return err
	}
	probe.store = liveStore
	return nil
}

func (*sessionStoreProbe) Dispose(context.Context) error { return nil }

func (persistenceFailureReporter) ReportEventFailure(context.Context, plugin.EventFailure) {}

func (persistenceFailureReporter) ReportPostCommitFailure(session.PostCommitFailure) {}

func (persistenceFailureReporter) ReportBackgroundWriteFailure(
	sesspersist.BackgroundWriteFailure,
) {
}

type countingBackend struct {
	sesspersist.Backend
	mutex     sync.Mutex
	loads     int
	seeks     int
	revisions int
}

func (storage *countingBackend) LoadStored(
	requestContext context.Context,
	identifier session.SessionID,
) (sesspersist.StoredPrefix, bool, error) {
	storage.mutex.Lock()
	storage.loads++
	storage.mutex.Unlock()
	return storage.Backend.LoadStored(requestContext, identifier)
}

func (storage *countingBackend) ReadStoredRevision(
	requestContext context.Context,
	identifier session.SessionID,
) (sesspersist.Revision, bool, error) {
	storage.mutex.Lock()
	storage.revisions++
	storage.mutex.Unlock()
	return storage.Backend.ReadStoredRevision(requestContext, identifier)
}

func (storage *countingBackend) LoadStoredFrom(
	requestContext context.Context,
	identifier session.SessionID,
	fromSeq int64,
) (sesspersist.StoredSuffix, bool, error) {
	storage.mutex.Lock()
	storage.seeks++
	storage.mutex.Unlock()
	return storage.Backend.LoadStoredFrom(requestContext, identifier, fromSeq)
}

func (storage *countingBackend) counts() (int, int, int) {
	storage.mutex.Lock()
	defer storage.mutex.Unlock()
	return storage.loads, storage.seeks, storage.revisions
}

type sqliteBackendOpener struct {
	settings sesssqlite.Config
	counted  bool
	opened   *countingBackend
}

func (opener *sqliteBackendOpener) OpenBackend(
	requestContext context.Context,
) (sesspersist.Backend, error) {
	storage, err := sesssqlite.Open(requestContext, opener.settings)
	if err != nil {
		return nil, err
	}
	if !opener.counted {
		return storage, nil
	}
	opener.opened = &countingBackend{
		Backend: storage,
	}
	return opener.opened, nil
}

type persistenceFixture struct {
	runtime     *plugin.Runtime
	store       session.LiveStore
	persistence *sesspersist.SessionLogStore
	opener      *sqliteBackendOpener
}

func newPersistenceFixture(
	testingContext *testing.T,
	path string,
	counted bool,
	cacheSize int,
) persistenceFixture {
	testingContext.Helper()
	reporter := persistenceFailureReporter{}
	sessionPlugin, err := session.NewPlugin(
		session.MemoryStoreOptions{
			PostCommitFailures: reporter,
		},
	)
	if err != nil {
		testingContext.Fatal(err)
	}
	opener := &sqliteBackendOpener{
		settings: sesssqlite.Config{
			Path:        path,
			JournalMode: sesssqlite.JournalWAL,
		},
		counted: counted,
	}
	durability, err := sesspersist.NewSessionLogStore(
		opener,
		sesspersist.SessionLogStoreOptions{
			WriteBatchMaxDelay:       time.Hour,
			PreparedSessionCacheSize: cacheSize,
			BackgroundWriteFailures:  reporter,
		},
	)
	if err != nil {
		testingContext.Fatal(err)
	}
	runtimeEngine := plugin.NewRuntime(
		plugin.RuntimeSettings{
			EventFailures: reporter,
		},
	)
	storeProbe := &sessionStoreProbe{}
	if _, err := runtimeEngine.Start(
		context.Background(),
		durability,
		sessionPlugin,
		storeProbe,
	); err != nil {
		testingContext.Fatal(err)
	}
	return persistenceFixture{
		runtime:     runtimeEngine,
		store:       storeProbe.store,
		persistence: durability,
		opener:      opener,
	}
}

func (fixture persistenceFixture) close(testingContext *testing.T) {
	testingContext.Helper()
	if err := fixture.runtime.Shutdown(context.Background()); err != nil {
		testingContext.Fatal(err)
	}
}

func TestSessionLogStorePersistsAnOpenTurnAndRepairsItOnColdLoad(t *testing.T) {
	t.Parallel()
	requestContext := context.Background()
	path := t.TempDir() + "/sessions.sqlite"
	first := newPersistenceFixture(t, path, false, 0)
	identifier := session.SessionID("cold-recovery")
	handle, err := first.store.Create(
		requestContext,
		&identifier,
		session.CreateOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	conversation := handle.Session()
	{
		draft, err := session.NewEventDraft(session.TurnStarted,
			session.TurnStart{
				Turn: 1,
			})
		if err == nil {
			_, err = conversation.Commit(context.Background(), session.Batch(draft))
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := first.store.Flush(requestContext, conversation); err != nil {
		t.Fatal(err)
	}
	first.close(t)

	second := newPersistenceFixture(t, path, false, 0)
	loaded, err := second.persistence.Load(requestContext, identifier)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Events) != 2 || loaded.Events[0].Type != session.TurnStartEventName ||
		loaded.Events[1].Type != session.TurnEndEventName {
		t.Fatalf("cold recovered events = %#v", loaded.Events)
	}
	prepared, err := second.persistence.Prepare(requestContext, identifier)
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Dispose()
	if prepared.UnpublishedSession().FirstLiveSeq() != 2 ||
		len(prepared.UnpublishedSession().Events()) != 3 ||
		prepared.UnpublishedSession().Events()[2].Type != session.EndSeedEventName {
		t.Fatalf("prepared Session = %#v", prepared.UnpublishedSession().Events())
	}
	second.close(t)
}

func TestSessionLogStoreReusesRevisionCurrentPreparationAndSeeksReadFrom(t *testing.T) {
	t.Parallel()
	requestContext := context.Background()
	path := t.TempDir() + "/sessions.sqlite"
	storage, err := sesssqlite.Open(
		requestContext,
		sesssqlite.Config{
			Path:        path,
			JournalMode: sesssqlite.JournalWAL,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	metadata, entries := persistenceClosedTurn(t, "cached-session")
	if err := storage.AppendBatch(requestContext, metadata, entries, false); err != nil {
		t.Fatal(err)
	}
	if err := storage.Close(requestContext); err != nil {
		t.Fatal(err)
	}

	fixture := newPersistenceFixture(t, path, true, 1)
	first, err := fixture.persistence.Inspect(requestContext, metadata.ID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := fixture.persistence.Inspect(requestContext, metadata.ID)
	if err != nil {
		t.Fatal(err)
	}
	loads, seeks, revisions := fixture.opener.opened.counts()
	if len(first.Events) != 2 || len(second.Events) != 2 || loads != 1 || seeks != 0 ||
		revisions < 1 {
		t.Fatalf(
			"cached inspections = (%d, %d), backend counts = (%d, %d, %d)",
			len(first.Events),
			len(second.Events),
			loads,
			seeks,
			revisions,
		)
	}
	prepared, err := fixture.persistence.Prepare(requestContext, metadata.ID)
	if err != nil {
		t.Fatal(err)
	}
	firstPrepared := prepared.UnpublishedSession()
	prepared.Dispose()
	preparedAgain, err := fixture.persistence.Prepare(requestContext, metadata.ID)
	if err != nil {
		t.Fatal(err)
	}
	if preparedAgain.UnpublishedSession() != firstPrepared {
		t.Fatal("revision-current prepared Session object was rebuilt")
	}
	preparedAgain.Dispose()
	loads, _, _ = fixture.opener.opened.counts()
	if loads != 1 {
		t.Fatalf("prepared cache performed %d full loads, want 1", loads)
	}
	external := session.Event{
		Type:      "extension/external",
		Seq:       2,
		Time:      3,
		Data:      []byte(`{}`),
		Ignorable: true,
	}
	if err := fixture.opener.opened.AppendBatch(
		requestContext,
		metadata,
		[]session.Event{external},
		true,
	); err != nil {
		t.Fatal(err)
	}
	refreshed, err := fixture.persistence.Inspect(requestContext, metadata.ID)
	if err != nil {
		t.Fatal(err)
	}
	loads, _, _ = fixture.opener.opened.counts()
	if len(refreshed.Events) != 3 || loads != 2 {
		t.Fatalf("revision refresh = %#v, full loads = %d", refreshed.Events, loads)
	}
	suffix, err := fixture.persistence.ReadFrom(requestContext, metadata.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	loadsAfterSeek, seeksAfterRead, _ := fixture.opener.opened.counts()
	if len(suffix.Events) != 2 || suffix.Events[0].Seq != 1 || suffix.Events[1].Seq != 2 ||
		loadsAfterSeek != loads || seeksAfterRead != 1 {
		t.Fatalf(
			"seek suffix = %#v, backend counts = (%d, %d)",
			suffix,
			loadsAfterSeek,
			seeksAfterRead,
		)
	}
	fixture.close(t)
}

func persistenceClosedTurn(
	testingContext *testing.T,
	identifier session.SessionID,
) (session.Header, []session.Event) {
	testingContext.Helper()
	conversation, err := session.New(identifier, session.CreateOptions{})
	if err != nil {
		testingContext.Fatal(err)
	}
	{
		draft, err := session.NewEventDraft(session.TurnStarted,
			session.TurnStart{
				Turn: 1,
			})
		if err == nil {
			_, err = conversation.Commit(context.Background(), session.Batch(draft))
		}
		if err != nil {
			testingContext.Fatal(err)
		}
	}
	{
		draft, err := session.NewEventDraft(session.TurnEnded,
			session.TurnEnd{
				Turn:   1,
				Reason: session.TurnCompleted{},
			})
		if err == nil {
			_, err = conversation.Commit(context.Background(), session.Batch(draft))
		}
		if err != nil {
			testingContext.Fatal(err)
		}
	}
	return conversation.Header(), conversation.Events()
}
