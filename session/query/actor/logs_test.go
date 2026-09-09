package queryactor_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	"github.com/gorenx/goren/session/persistence"
	storage "github.com/gorenx/goren/session/persistence/sqlite"
	"github.com/gorenx/goren/session/query"
	queryactor "github.com/gorenx/goren/session/query/actor"
	"github.com/gorenx/goren/session/query/sqlite"
	sessionregistry "github.com/gorenx/goren/session/registry"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
	"github.com/tochemey/goakt/v4/reentrancy"
)

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

func TestQueryReadsLiveAndPagedDurableSourcesBeforeReply(t *testing.T) {
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
	history, err := persistence.NewReader(backend)
	if err != nil {
		t.Fatal(err)
	}
	observations, err := queryactor.NewLogs(system, directory, history, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := queryactor.New(index, query.Config{}, observations)
	if err != nil {
		t.Fatal(err)
	}
	target, err := system.Spawn(ctx, "query", owner, goakt.WithStashing())
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
	listed := request[queryactor.Sessions](t, driver, replies, queryactor.ListSessions{})
	if listed.Err != nil || len(listed.Items) != 130 {
		t.Fatalf("list = %d, %v", len(listed.Items), listed.Err)
	}
	// A stopped source must fail this request instead of returning old observations.
	if err := backend.Close(ctx); err != nil {
		t.Fatal(err)
	}
	failed := request[queryactor.Sessions](t, driver, replies, queryactor.ListSessions{})
	if failed.Err == nil {
		t.Fatal("failed source returned stale observation")
	}
}
