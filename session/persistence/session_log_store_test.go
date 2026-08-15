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

type testSessionProvider struct{}

func (testSessionProvider) Manifest() plugin.Manifest {
	return plugin.Manifest{Name: "test-sessions", Provides: []plugin.ServiceRef{session.StoreService.Ref()}}
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

type cacheFixture struct {
	path        string
	backend     *countingBackend
	persistence sesspersist.Persistence
}

func (*cacheFixture) Manifest() plugin.Manifest {
	return plugin.Manifest{Name: "session-persistence-cache-fixture"}
}

func (fixture *cacheFixture) Apply(requestContext context.Context, pluginScope *plugin.Scope) error {
	storeProvider, err := session.NewMemoryStore(pluginScope, session.MemoryStoreOptions{})
	if err != nil {
		return err
	}
	if err := pluginScope.Effect(requestContext, "sessions", func(context.Context) (plugin.Disposer, error) {
		return storeProvider.Close, nil
	}); err != nil {
		return err
	}
	storage, err := sesssqlite.Open(requestContext, sesssqlite.Config{Path: fixture.path, JournalMode: sesssqlite.JournalWAL})
	if err != nil {
		return err
	}
	fixture.backend = &countingBackend{Backend: storage}
	durability, err := sesspersist.NewSessionLogStore(
		requestContext, pluginScope, storeProvider, fixture.backend,
		sesspersist.SessionLogStoreOptions{WriteBatchMaxDelay: time.Hour, PreparedSessionCacheSize: 1},
	)
	if err != nil {
		_ = storage.Close(requestContext)
		return err
	}
	fixture.persistence = durability
	return nil
}

func (testSessionProvider) Apply(requestContext context.Context, pluginScope *plugin.Scope) error {
	store, err := session.NewMemoryStore(pluginScope, session.MemoryStoreOptions{})
	if err != nil {
		return err
	}
	if err := pluginScope.Effect(requestContext, "test sessions", func(context.Context) (plugin.Disposer, error) {
		return store.Close, nil
	}); err != nil {
		return err
	}
	_, err = plugin.Provide(pluginScope, session.StoreService, session.Store(store))
	return err
}

type testPersistenceProvider struct {
	path string
}

func (testPersistenceProvider) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "test-session-persistence", Provides: []plugin.ServiceRef{sesspersist.Service.Ref()},
		Requires: []plugin.ServiceRef{session.StoreService.Ref()},
	}
}

func (instance testPersistenceProvider) Apply(requestContext context.Context, pluginScope *plugin.Scope) error {
	store, found := plugin.Require(pluginScope, session.StoreService)
	if !found {
		return context.Canceled
	}
	storage, err := sesssqlite.Open(requestContext, sesssqlite.Config{
		Path: instance.path, JournalMode: sesssqlite.JournalWAL,
	})
	if err != nil {
		return err
	}
	durability, err := sesspersist.NewSessionLogStore(
		requestContext, pluginScope, store, storage,
		sesspersist.SessionLogStoreOptions{WriteBatchMaxDelay: time.Hour},
	)
	if err != nil {
		_ = storage.Close(requestContext)
		return err
	}
	_, err = plugin.Provide(pluginScope, sesspersist.Service, sesspersist.Persistence(durability))
	return err
}

type testProbe struct {
	body func(context.Context, *plugin.Scope, session.Store, sesspersist.Persistence) error
}

func (testProbe) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name:     "test-persistence-probe",
		Requires: []plugin.ServiceRef{session.StoreService.Ref(), sesspersist.Service.Ref()},
	}
}

func (instance testProbe) Apply(requestContext context.Context, pluginScope *plugin.Scope) error {
	store, sessionsFound := plugin.Require(pluginScope, session.StoreService)
	durability, persistenceFound := plugin.Require(pluginScope, sesspersist.Service)
	if !sessionsFound || !persistenceFound {
		return context.Canceled
	}
	return instance.body(requestContext, pluginScope, store, durability)
}

func TestSessionLogStorePersistsAnOpenTurnAndRepairsItOnColdLoad(t *testing.T) {
	t.Parallel()
	requestContext := context.Background()
	path := t.TempDir() + "/sessions.sqlite"
	firstRuntime := plugin.NewRuntime()
	if _, err := firstRuntime.Load(requestContext, testSessionProvider{}); err != nil {
		t.Fatal(err)
	}
	if _, err := firstRuntime.Load(requestContext, testPersistenceProvider{path: path}); err != nil {
		t.Fatal(err)
	}
	if _, err := firstRuntime.Load(requestContext, testProbe{body: func(
		operationContext context.Context,
		pluginScope *plugin.Scope,
		store session.Store,
		_ sesspersist.Persistence,
	) error {
		identifier := session.SessionID("cold-recovery")
		conversation, err := store.Create(operationContext, pluginScope, &identifier, session.CreateOptions{})
		if err != nil {
			return err
		}
		if _, err := session.Append(conversation, session.TurnStarted, session.TurnStart{Turn: 1}); err != nil {
			return err
		}
		_, err = store.Flush(operationContext, conversation)
		return err
	}}); err != nil {
		t.Fatal(err)
	}
	if err := firstRuntime.Shutdown(requestContext); err != nil {
		t.Fatal(err)
	}

	secondRuntime := plugin.NewRuntime()
	if _, err := secondRuntime.Load(requestContext, testSessionProvider{}); err != nil {
		t.Fatal(err)
	}
	if _, err := secondRuntime.Load(requestContext, testPersistenceProvider{path: path}); err != nil {
		t.Fatal(err)
	}
	if _, err := secondRuntime.Load(requestContext, testProbe{body: func(
		operationContext context.Context,
		_ *plugin.Scope,
		_ session.Store,
		durability sesspersist.Persistence,
	) error {
		loaded, err := durability.Load(operationContext, "cold-recovery")
		if err != nil {
			return err
		}
		if len(loaded.Events) != 2 || loaded.Events[0].Type != session.TurnStartEventName ||
			loaded.Events[1].Type != session.TurnEndEventName {
			t.Fatalf("cold recovered events = %#v", loaded.Events)
		}
		prepared, err := durability.Prepare(operationContext, "cold-recovery")
		if err != nil {
			return err
		}
		defer prepared.Dispose()
		if prepared.UnpublishedSession().FirstLiveSeq() != 2 || len(prepared.UnpublishedSession().Events()) != 3 ||
			prepared.UnpublishedSession().Events()[2].Type != session.EndSeedEventName {
			t.Fatalf("prepared Session = %#v", prepared.UnpublishedSession().Events())
		}
		return nil
	}}); err != nil {
		t.Fatal(err)
	}
	if err := secondRuntime.Shutdown(requestContext); err != nil {
		t.Fatal(err)
	}
}

func TestSessionLogStoreReusesRevisionCurrentPreparationAndSeeksReadFrom(t *testing.T) {
	t.Parallel()
	requestContext := context.Background()
	path := t.TempDir() + "/sessions.sqlite"
	storage, err := sesssqlite.Open(requestContext, sesssqlite.Config{Path: path, JournalMode: sesssqlite.JournalWAL})
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

	engine := plugin.NewRuntime()
	fixture := &cacheFixture{path: path}
	handle, err := engine.Load(requestContext, fixture)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Unload(requestContext, handle) })
	first, err := fixture.persistence.Inspect(requestContext, metadata.ID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := fixture.persistence.Inspect(requestContext, metadata.ID)
	if err != nil {
		t.Fatal(err)
	}
	loads, seeks, revisions := fixture.backend.counts()
	if len(first.Events) != 2 || len(second.Events) != 2 || loads != 1 || seeks != 0 || revisions < 1 {
		t.Fatalf("cached inspections = (%d, %d), backend counts = (%d, %d, %d)",
			len(first.Events), len(second.Events), loads, seeks, revisions)
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
	loads, _, _ = fixture.backend.counts()
	if loads != 1 {
		t.Fatalf("prepared cache performed %d full loads, want 1", loads)
	}
	external := session.Event{
		Type: "extension/external", Seq: 2, Time: 3, Data: []byte(`{}`), Ignorable: true,
	}
	if err := fixture.backend.AppendBatch(requestContext, metadata, []session.Event{external}, true); err != nil {
		t.Fatal(err)
	}
	refreshed, err := fixture.persistence.Inspect(requestContext, metadata.ID)
	if err != nil {
		t.Fatal(err)
	}
	loads, _, _ = fixture.backend.counts()
	if len(refreshed.Events) != 3 || loads != 2 {
		t.Fatalf("revision refresh = %#v, full loads = %d", refreshed.Events, loads)
	}
	suffix, err := fixture.persistence.ReadFrom(requestContext, metadata.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	loadsAfterSeek, seeksAfterRead, _ := fixture.backend.counts()
	if len(suffix.Events) != 2 || suffix.Events[0].Seq != 1 || suffix.Events[1].Seq != 2 ||
		loadsAfterSeek != loads || seeksAfterRead != 1 {
		t.Fatalf("seek suffix = %#v, backend counts = (%d, %d)", suffix, loadsAfterSeek, seeksAfterRead)
	}
}

func persistenceClosedTurn(t *testing.T, identifier session.SessionID) (session.Header, []session.Event) {
	t.Helper()
	conversation, err := session.New(identifier, session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.Append(conversation, session.TurnStarted, session.TurnStart{Turn: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Append(conversation, session.TurnEnded, session.TurnEnd{
		Turn: 1, Reason: session.TurnCompleted{},
	}); err != nil {
		t.Fatal(err)
	}
	return conversation.Header(), conversation.Events()
}
