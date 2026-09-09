package workspaceactor

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	"github.com/gorenx/goren/session/persistence"
	sessionsqlite "github.com/gorenx/goren/session/persistence/sqlite"
	sessionregistry "github.com/gorenx/goren/session/registry"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
	"github.com/tochemey/goakt/v4/reentrancy"
)

func TestHeadersMergeLiveAndDurableSessions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	storage, err := sessionsqlite.Open(ctx, sessionsqlite.Config{
		Path:        filepath.Join(t.TempDir(), "sessions.sqlite"),
		JournalMode: sessionsqlite.JournalWAL,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close(context.Background())
	system, err := goakt.NewActorSystem("workspace-headers", goakt.WithLogger(log.DiscardLogger))
	if err != nil {
		t.Fatal(err)
	}
	if err := system.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer system.Stop(context.Background())
	registry, err := goakt.GrainOf[*sessionregistry.RegistryGrain](ctx, system, sessionregistry.RegistryName,
		goakt.WithLongLivedGrain(),
		goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		t.Fatal(err)
	}
	durable, err := persistence.NewReader(storage)
	if err != nil {
		t.Fatal(err)
	}
	source, err := NewHeaders(system, registry, durable, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ask := func(target *goakt.GrainIdentity, input any) any {
		t.Helper()
		payload, err := system.AskGrain(ctx, target, input, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		return payload
	}
	root := "/project"
	for _, identifier := range []session.SessionID{"a", "b"} {
		opened := ask(registry, sessionregistry.CreateSession{
			SessionID:  identifier,
			CreationID: string(identifier),
			Storage:    storage,
			Options: session.CreateOptions{
				Metadata: session.Metadata{
					CWD: &root,
				},
			},
		}).(sessionactor.Initialized)
		if opened.Err != nil {
			t.Fatal(opened.Err)
		}
	}
	header, err := source.Get(ctx, "a")
	if err != nil || header == nil || header.CWD == nil || *header.CWD != root {
		t.Fatalf("live header: %#v, %v", header, err)
	}
	*header.CWD = "changed"
	header, err = source.Get(ctx, "a")
	if err != nil || *header.CWD != root {
		t.Fatal("header aliases live owner")
	}
	if _, err := durable.Inspect(ctx, "a"); err == nil {
		t.Fatal("empty Session unexpectedly persisted")
	}
	endpoint := ask(registry, sessionregistry.ResolveSession{
		SessionID:  "b",
		CreationID: "b",
	}).(sessionregistry.Endpoint)
	if endpoint.Err != nil {
		t.Fatal(endpoint.Err)
	}
	draft, err := session.NewEventDraft(session.TurnStarted, session.TurnStart{
		Turn: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	committed := ask(endpoint.Target, sessionactor.Commit{
		CreationID: "b",
		Plan:       session.Batch(draft),
	}).(sessionactor.Committed)
	if committed.Err != nil {
		t.Fatal(committed.Err)
	}
	closed := ask(registry, sessionregistry.CloseSession{
		SessionID:  "b",
		CreationID: "b",
	}).(sessionactor.Closed)
	if closed.Err != nil {
		t.Fatal(closed.Err)
	}
	header, err = source.Get(ctx, "b")
	if err != nil || header == nil || header.ID != "b" {
		t.Fatalf("durable header: %#v %v", header, err)
	}
	resumed := ask(registry, sessionregistry.ResumeSession{
		SessionID:  "b",
		CreationID: "resumed",
		Storage:    storage,
	}).(sessionactor.Initialized)
	if resumed.Err != nil {
		t.Fatal(resumed.Err)
	}
	listing, err := source.List(ctx)
	if err != nil || len(listing) != 2 || listing[0].ID != "a" || listing[1].ID != "b" {
		t.Fatalf("merged list: %#v %v", listing, err)
	}
	missing, err := source.Get(ctx, "missing")
	if err != nil || missing != nil {
		t.Fatalf("missing: %+v %v", missing, err)
	}
	canceled, stop := context.WithCancel(ctx)
	stop()
	cancelled, err := source.Get(canceled, "a")
	if !errors.Is(err, context.Canceled) || cancelled != nil {
		t.Fatalf("cancellation: %+v %v", cancelled, err)
	}
	for _, identity := range []struct {
		id       session.SessionID
		creation string
	}{
		{
			id:       "a",
			creation: "a",
		},
		{
			id:       "b",
			creation: "resumed",
		},
	} {
		closed := ask(registry, sessionregistry.CloseSession{
			SessionID:  identity.id,
			CreationID: identity.creation,
		}).(sessionactor.Closed)
		if closed.Err != nil {
			t.Fatal(closed.Err)
		}
	}
}
