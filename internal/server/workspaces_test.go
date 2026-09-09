package server

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	workspaceadapter "github.com/gorenx/goren/apiproxy/workspace/actor"
	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	"github.com/gorenx/goren/session/persistence"
	sessionsqlite "github.com/gorenx/goren/session/persistence/sqlite"
	sessionregistry "github.com/gorenx/goren/session/registry"
	"github.com/gorenx/goren/workspace"
	workspaceactor "github.com/gorenx/goren/workspace/actor"
	"github.com/gorenx/goren/workspace/persistence/sqlite"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
	"github.com/tochemey/goakt/v4/reentrancy"
)

func TestWorkspaceCompositionRecoversMembershipFromDurableSessionHeaders(t *testing.T) {
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
	system, err := goakt.NewActorSystem("workspace-composition", goakt.WithPubSub(), goakt.WithLogger(log.DiscardLogger))
	if err != nil {
		t.Fatal(err)
	}
	err = system.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer system.Stop(context.Background())
	sessions, err := goakt.GrainOf[*sessionregistry.RegistryGrain](ctx, system, sessionregistry.RegistryName,
		goakt.WithLongLivedGrain(),
		goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		t.Fatal(err)
	}
	history, err := persistence.NewReader(storage)
	if err != nil {
		t.Fatal(err)
	}
	settings := sqlite.Config{
		Path:        filepath.Join(t.TempDir(), "workspaces.sqlite"),
		JournalMode: sqlite.JournalWAL,
	}
	start := func() (*goakt.PID, *workspaceadapter.Directory) {
		t.Helper()
		endpoint, failure := initializeWorkspaces(ctx, system, sessions, history, settings, time.Second)
		if failure != nil {
			t.Fatal(failure)
		}
		client, failure := workspaceactor.NewClient(endpoint, time.Second)
		if failure != nil {
			t.Fatal(failure)
		}
		directory, failure := workspaceadapter.NewDirectory(client)
		if failure != nil {
			t.Fatal(failure)
		}
		return endpoint, directory
	}
	endpoint, directory := start()
	root := t.TempDir()
	created, _, err := directory.Create(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []struct {
		id   session.SessionID
		path string
	}{
		{
			id:   "matching",
			path: root,
		},
		{
			id:   "different",
			path: t.TempDir(),
		},
	} {
		opened := runtimeRequest[sessionactor.Initialized](t, ctx, system, sessions, sessionregistry.CreateSession{
			SessionID:  input.id,
			CreationID: string(input.id),
			Storage:    storage,
			Options: session.CreateOptions{
				Metadata: session.Metadata{
					CWD: &input.path,
				},
			},
		})
		if opened.Err != nil {
			t.Fatal(opened.Err)
		}
	}
	_, err = history.Inspect(ctx, "matching")
	var missing *persistence.NotFoundError
	if !errors.As(err, &missing) {
		t.Fatalf("expected an unpersisted empty Session: %v", err)
	}
	err = directory.AttachSession(ctx, created.ID, "matching")
	if err != nil {
		t.Fatal(err)
	}
	err = directory.AttachSession(ctx, created.ID, "different")
	var refused *workspace.AttachError
	if !errors.As(err, &refused) {
		t.Fatalf("mismatched CWD was accepted: %v", err)
	}
	_, err = directory.ArchiveSession(ctx, "matching")
	if err != nil {
		t.Fatal(err)
	}
	for _, identifier := range []session.SessionID{"matching", "different"} {
		route := runtimeRequest[sessionregistry.Endpoint](t, ctx, system, sessions, sessionregistry.ResolveSession{
			SessionID:  identifier,
			CreationID: string(identifier),
		})
		if route.Err != nil {
			t.Fatal(route.Err)
		}
		draft, failure := session.NewEventDraft(session.TurnStarted, session.TurnStart{
			Turn: 1,
		})
		if failure != nil {
			t.Fatal(failure)
		}
		committed := runtimeRequest[sessionactor.Committed](t, ctx, system, route.Target, sessionactor.Commit{
			CreationID: string(identifier),
			Plan:       session.Batch(draft),
		})
		if committed.Err != nil {
			t.Fatal(committed.Err)
		}
		closed := runtimeRequest[sessionactor.Closed](t, ctx, system, sessions, sessionregistry.CloseSession{
			SessionID:  identifier,
			CreationID: string(identifier),
		})
		if closed.Err != nil {
			t.Fatal(closed.Err)
		}
	}
	err = endpoint.Shutdown(ctx)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, directory = start()
	defer endpoint.Shutdown(context.Background())
	retained, err := directory.Read(ctx, created.ID)
	if err != nil || retained == nil || !slices.Equal(retained.SessionIDs, []session.SessionID{"matching"}) {
		t.Fatalf("restored membership: %+v, %v", retained, err)
	}
	listing, err := directory.List(ctx)
	if err != nil || !slices.Equal(listing.ArchivedSessionIDs, []session.SessionID{"matching"}) {
		t.Fatalf("restored archive: %+v, %v", listing, err)
	}
}
