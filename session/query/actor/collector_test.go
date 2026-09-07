package queryactor_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	"github.com/gorenx/goren/session/persistence"
	persistenceactor "github.com/gorenx/goren/session/persistence/actor"
	storage "github.com/gorenx/goren/session/persistence/sqlite"
	"github.com/gorenx/goren/session/query"
	queryactor "github.com/gorenx/goren/session/query/actor"
	"github.com/gorenx/goren/session/query/sqlite"
	sessionregistry "github.com/gorenx/goren/session/registry"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
	"github.com/tochemey/goakt/v4/reentrancy"
)

type collect struct {
	registry    *goakt.GrainIdentity
	persistence *goakt.PID
	target      *goakt.PID
}

type collectionHost struct{}

func (*collectionHost) OnActivate(context.Context, *goakt.GrainProps) error   { return nil }
func (*collectionHost) OnDeactivate(context.Context, *goakt.GrainProps) error { return nil }
func (*collectionHost) OnReceive(gc *goakt.GrainContext) {
	input := gc.Message().(collect)
	reply := gc.DeferResponse()
	_, err := gc.ActorSystem().Spawn(gc.Context(), "collector-"+uuid.NewString(), &queryactor.Collector{
		Registry:    input.registry,
		Persistence: input.persistence,
		Target:      input.target,
		Reply:       reply,
	}, goakt.WithReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		reply.Response(queryactor.Completion{
			Err: err,
		})
	}
}

type grainRequester struct {
	target  *goakt.GrainIdentity
	replies chan response
}

func (*grainRequester) PreStart(*goakt.Context) error { return nil }
func (*grainRequester) PostStop(*goakt.Context) error { return nil }
func (driver *grainRequester) Receive(rc *goakt.ReceiveContext) {
	if input, ok := rc.Message().(command); ok {
		replies := driver.replies
		rc.RequestGrain(driver.target, input.payload).Then(func(value any, err error) {
			replies <- response{
				value: value,
				err:   err,
			}
		})
	}
}
func grainDriver(t *testing.T, system goakt.ActorSystem, identity *goakt.GrainIdentity) (*goakt.PID, <-chan response) {
	t.Helper()
	replies := make(chan response, 4)
	driver, err := system.Spawn(context.Background(), "requester-"+uuid.NewString(), &grainRequester{
		target:  identity,
		replies: replies,
	}, goakt.WithReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		t.Fatal(err)
	}
	return driver, replies
}

func TestCollectorPublishesLiveAndPagedDurableSourcesAtomically(t *testing.T) {
	ctx := context.Background()
	backend, err := storage.Open(ctx, storage.Config{
		Path:        filepath.Join(t.TempDir(), "sessions.sqlite"),
		JournalMode: storage.JournalWAL,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := backend.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	// More than one collector page must be read before publishing.
	for position := 0; position < 129; position++ {
		conversation, err := session.NewLog(session.SessionID(fmt.Sprintf("cold-%03d", position)), session.CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if err := backend.AppendBatch(ctx, persistence.EventBatch{
			Header:      conversation.Header(),
			Events:      conversation.Events(),
			Materialize: true,
		}); err != nil {
			t.Fatal(err)
		}
	}
	index, err := sqlite.Open(ctx, sqlite.Config{
		Path: filepath.Join(t.TempDir(), "query.sqlite"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := index.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	system, err := goakt.NewActorSystem("collection-test", goakt.WithLogger(log.DiscardLogger))
	if err != nil {
		t.Fatal(err)
	}
	if err := system.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := system.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	directory, err := goakt.GrainOf[*sessionregistry.RegistryGrain](ctx, system, sessionregistry.RegistryName, goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	registryDriver, registryReplies := grainDriver(t, system, directory)
	opened := request[sessionactor.Initialized](t, registryDriver, registryReplies, sessionregistry.CreateSession{
		SessionID:  "live",
		CreationID: "current",
		Storage:    backend,
	})
	if opened.Err != nil {
		t.Fatal(opened.Err)
	}
	reader, err := persistenceactor.New(backend)
	if err != nil {
		t.Fatal(err)
	}
	durable, err := system.Spawn(ctx, "persistence", reader)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := queryactor.New(index, query.Config{})
	if err != nil {
		t.Fatal(err)
	}
	target, err := system.Spawn(ctx, "query", owner)
	if err != nil {
		t.Fatal(err)
	}
	replies := make(chan response, 4)
	driver, err := system.Spawn(ctx, "query-requester", &requester{
		target:  target,
		replies: replies,
	}, goakt.WithReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		t.Fatal(err)
	}
	host, err := goakt.GrainOf[*collectionHost](ctx, system, "collection", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	collectorDriver, collectorReplies := grainDriver(t, system, host)
	input := collect{
		registry:    directory,
		persistence: durable,
		target:      target,
	}
	completed := request[queryactor.Completion](t, collectorDriver, collectorReplies, input)
	if completed.Err != nil {
		t.Fatal(completed.Err)
	}
	listed := request[queryactor.Sessions](t, driver, replies, queryactor.ListSessions{})
	if listed.Err != nil || len(listed.Items) != 130 {
		t.Fatalf("list = %d, %v", len(listed.Items), listed.Err)
	}
	// A failed collection cannot erase the previously published observation.
	if err := system.Kill(ctx, "persistence"); err != nil {
		t.Fatal(err)
	}
	completed = request[queryactor.Completion](t, collectorDriver, collectorReplies, input)
	if completed.Err == nil {
		t.Fatal("stopped source reported success")
	}
	listed = request[queryactor.Sessions](t, driver, replies, queryactor.ListSessions{})
	if listed.Err != nil || len(listed.Items) != 130 {
		t.Fatal("failed collection replaced the observation")
	}
	live := request[queryactor.Log](t, driver, replies, queryactor.ReadSession{
		SessionID: "live",
	})
	if live.Err != nil || live.Value.Header.ID != "live" {
		t.Fatalf("live = %#v", live)
	}
	closed := request[sessionactor.Closed](t, registryDriver, registryReplies, sessionregistry.CloseSession{
		SessionID:  "live",
		CreationID: "current",
	})
	if closed.Err != nil {
		t.Fatal(closed.Err)
	}
}
