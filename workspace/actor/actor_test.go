package workspaceactor

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/workspace"
	"github.com/gorenx/goren/workspace/persistence/sqlite"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
)

type headers struct{ root string }

func (source headers) Get(_ context.Context, id session.SessionID) (session.Header, bool, error) {
	return session.Header{
		Version:   session.FormatVersion,
		ID:        id,
		CreatedAt: 1,
		CWD:       &source.root,
	}, true, nil
}
func (headers) List(context.Context) ([]session.Header, error) { return nil, nil }

func TestConcurrentAttachIsSerializedAndDurable(t *testing.T) {
	ctx := context.Background()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	storage, err := sqlite.Open(ctx, sqlite.Config{
		Path:        filepath.Join(t.TempDir(), "workspaces.sqlite"),
		JournalMode: sqlite.JournalWAL,
	})
	if err != nil {
		t.Fatal(err)
	}
	system, err := goakt.NewActorSystem("workspace-test", goakt.WithLogger(log.DiscardLogger))
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
	target, err := Spawn(ctx, system, "workspace", storage, workspace.RegistryOptions{
		SessionHeads: headers{
			root: root,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := NewClient(target, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := endpoint.execute(ctx, Create{
		Path: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	created := payload.(Created)
	if created.Err != nil || !created.Created {
		t.Fatalf("create: %+v", created)
	}
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			payload, err := endpoint.execute(ctx, AttachSession{
				ID:        created.State.ID,
				SessionID: "s",
			})
			if err != nil {
				t.Error(err)
				return
			}
			if err := payload.(Completion).Err; err != nil {
				t.Error(err)
			}
		})
	}
	group.Wait()
	payload, err = endpoint.execute(ctx, Read{
		ID: created.State.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	view := payload.(View)
	if !view.Found || len(view.State.SessionIDs) != 1 || view.State.SessionIDs[0] != "s" {
		t.Fatalf("view: %+v", view)
	}
	stored, err := storage.Load(ctx)
	if err != nil || len(stored.Records) != 1 || len(stored.Records[0].SessionIDs) != 1 {
		t.Fatalf("durable state: %+v %v", stored, err)
	}
}
