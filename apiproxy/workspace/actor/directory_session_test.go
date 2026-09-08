package workspaceadapter_test

import (
	"context"
	"errors"
	workspaceadapter "github.com/gorenx/goren/apiproxy/workspace/actor"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/workspace"
	workspaceactor "github.com/gorenx/goren/workspace/actor"
	"github.com/gorenx/goren/workspace/persistence/sqlite"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
	"path/filepath"
	"testing"
	"time"
)

type membershipHeaders struct{ root string }

func (source membershipHeaders) Get(_ context.Context, id session.SessionID) (session.Header, bool, error) {
	if id != "known" {
		return session.Header{}, false, nil
	}
	return session.Header{
		ID:  id,
		CWD: &source.root,
	}, true, nil
}
func (membershipHeaders) List(context.Context) ([]session.Header, error) { return nil, nil }
func TestDirectoryAttachesSessionThroughWorkspaceOwner(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	storage, err := sqlite.Open(ctx, sqlite.Config{
		Path:        filepath.Join(t.TempDir(), "workspace.sqlite"),
		JournalMode: sqlite.JournalWAL,
	})
	if err != nil {
		t.Fatal(err)
	}
	system, err := goakt.NewActorSystem("workspace-api-test", goakt.WithLogger(log.DiscardLogger))
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
	target, err := workspaceactor.Spawn(ctx, system, "workspace", storage, workspace.RegistryOptions{
		SessionHeads: membershipHeaders{
			root: root,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := workspaceactor.NewClient(target, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	directory, err := workspaceadapter.NewDirectory(endpoint)
	if err != nil {
		t.Fatal(err)
	}

	current, _, err := directory.Create(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if absent, err := directory.Read(ctx, "missing"); err != nil || absent != nil {
		t.Fatalf("absent: %+v %v", absent, err)
	}
	for range 2 {
		if err := directory.AttachSession(ctx, current.ID, "known"); err != nil {
			t.Fatal(err)
		}
	}
	err = directory.AttachSession(ctx, current.ID, "unknown")
	var refused *workspace.AttachError
	if !errors.As(err, &refused) {
		t.Fatalf("unknown Session: %v", err)
	}
	view, err := directory.Read(ctx, current.ID)
	if err != nil || view == nil || len(view.SessionIDs) != 1 || view.SessionIDs[0] != "known" {
		t.Fatalf("membership: %+v %v", view, err)
	}
	durable, err := storage.Load(ctx)
	if err != nil || len(durable.Records) != 1 || len(durable.Records[0].SessionIDs) != 1 {
		t.Fatalf("durability: %+v %v", durable, err)
	}
}
