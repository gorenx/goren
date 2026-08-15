package persistence_test

import (
	"context"
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
