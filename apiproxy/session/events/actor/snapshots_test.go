package eventadapter_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	eventadapter "github.com/gorenx/goren/apiproxy/session/events/actor"
	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	"github.com/gorenx/goren/session/persistence/sqlite"
	sessionregistry "github.com/gorenx/goren/session/registry"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
	"github.com/tochemey/goakt/v4/reentrancy"
)

func TestSnapshotReadsPreserveCreationAuthority(t *testing.T) {
	ctx := context.Background()
	system, err := goakt.NewActorSystem("session-api-read-test", goakt.WithLogger(log.DiscardLogger))
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
	storage, err := sqlite.Open(ctx, sqlite.Config{
		Path:        filepath.Join(t.TempDir(), "sessions.sqlite"),
		JournalMode: sqlite.JournalWAL,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := storage.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	authority, err := goakt.GrainOf[*sessionregistry.RegistryGrain](ctx, system, sessionregistry.RegistryName, goakt.WithLongLivedGrain(), goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		t.Fatal(err)
	}
	source, err := eventadapter.NewSnapshots(system, authority, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.ReadSnapshot(ctx, "conversation", "first"); !errors.Is(err, sessionactor.ErrCreation) {
		t.Fatalf("absent read: %v", err)
	}
	directory := "/project"
	request := func(input any) any {
		t.Helper()
		payload, err := system.AskGrain(ctx, authority, input, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		return payload
	}
	create := func(creationID string) {
		t.Helper()
		payload := request(sessionregistry.CreateSession{
			SessionID:  "conversation",
			CreationID: creationID,
			Storage:    storage,
			Options: session.CreateOptions{
				Metadata: session.Metadata{
					CWD: &directory,
				},
			},
		})
		if failure := payload.(sessionactor.Initialized).Err; failure != nil {
			t.Fatal(failure)
		}
	}
	create("first")
	snapshots, err := source.ListSnapshots(ctx)
	if err != nil || len(snapshots) != 1 {
		t.Fatalf("baseline: %+v %v", snapshots, err)
	}
	if snapshots[0].CreationID != "first" || *snapshots[0].Header.CWD != directory {
		t.Fatalf("wrong baseline: %+v", snapshots[0])
	}
	*snapshots[0].Header.CWD = "/tampered"
	snapshot, err := source.ReadSnapshot(ctx, "conversation", "first")
	if err != nil || *snapshot.Header.CWD != directory {
		t.Fatalf("snapshot exposed owner: %+v %v", snapshot, err)
	}
	payload := request(sessionregistry.CloseSession{
		SessionID:  "conversation",
		CreationID: "first",
	})
	if failure := payload.(sessionactor.Closed).Err; failure != nil {
		t.Fatal(failure)
	}
	snapshots, err = source.ListSnapshots(ctx)
	if err != nil || len(snapshots) != 0 {
		t.Fatalf("closed baseline: %+v %v", snapshots, err)
	}
	// An empty Session never materialized. Its ID can be reused after complete close.
	create("second")
	if _, err := source.ReadSnapshot(ctx, "conversation", "first"); !errors.Is(err, sessionactor.ErrCreation) {
		t.Fatalf("old creation reached replacement: %v", err)
	}
	snapshot, err = source.ReadSnapshot(ctx, "conversation", "second")
	if err != nil || snapshot.CreationID != "second" {
		t.Fatalf("new creation: %+v %v", snapshot, err)
	}
	resolved := request(sessionregistry.ResolveSession{
		SessionID:  "conversation",
		CreationID: "second",
	}).(sessionregistry.Endpoint)
	if resolved.Err != nil {
		t.Fatal(resolved.Err)
	}
	draft, err := session.NewEventDraft(session.TurnStarted, session.TurnStart{
		Turn: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := system.AskGrain(ctx, resolved.Target, sessionactor.Commit{
		CreationID: "second",
		Plan:       session.Batch(draft),
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if failure := result.(sessionactor.Committed).Err; failure != nil {
		t.Fatal(failure)
	}
	snapshot, err = source.ReadSnapshot(ctx, "conversation", "second")
	if err != nil || len(snapshot.Events) != 1 || string(snapshot.Events[0].Data) != `{"turn":1}` {
		t.Fatalf("committed read: %+v %v", snapshot, err)
	}
	snapshot.Events[0].Data[0] = '!'
	snapshot, err = source.ReadSnapshot(ctx, "conversation", "second")
	if err != nil || string(snapshot.Events[0].Data) != `{"turn":1}` {
		t.Fatalf("event copy: %+v %v", snapshot, err)
	}
	closed := request(sessionregistry.CloseSession{
		SessionID:  "conversation",
		CreationID: "second",
	}).(sessionactor.Closed)
	if closed.Err != nil || closed.Barrier.NextSeq != 1 {
		t.Fatalf("final durability: %+v", closed)
	}

}
