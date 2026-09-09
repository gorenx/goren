package workspaceactor

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorenx/goren/workspace"
	"github.com/gorenx/goren/workspace/persistence/sqlite"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
)

type blockedStorage struct {
	workspace.Backend
	entered   chan struct{}
	cancelled chan struct{}
	release   chan struct{}
	closed    chan struct{}
}

func (storage *blockedStorage) Create(ctx context.Context, _ workspace.StoredWorkspace, _ []workspace.ID) error {
	close(storage.entered)
	<-ctx.Done()
	close(storage.cancelled)
	<-storage.release
	return context.Cause(ctx)
}

func (storage *blockedStorage) Close(ctx context.Context) error {
	close(storage.closed)
	return storage.Backend.Close(ctx)
}

func TestShutdownWaitsForWorkspaceOperationBeforeClosingStorage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	durable, err := sqlite.Open(ctx, sqlite.Config{
		Path:        filepath.Join(t.TempDir(), "workspace.sqlite"),
		JournalMode: sqlite.JournalWAL,
	})
	if err != nil {
		t.Fatal(err)
	}
	storage := &blockedStorage{
		Backend:   durable,
		entered:   make(chan struct{}),
		cancelled: make(chan struct{}),
		release:   make(chan struct{}),
		closed:    make(chan struct{}),
	}
	system, err := goakt.NewActorSystem("workspace-shutdown", goakt.WithLogger(log.DiscardLogger))
	if err != nil {
		t.Fatal(err)
	}
	if err := system.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer system.Stop(context.Background())
	target, err := Spawn(ctx, system, "workspace", storage, workspace.RegistryOptions{
		SessionHeads: headers{},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		select {
		case <-storage.release:
		default:
			close(storage.release)
		}
	}()
	endpoint, err := NewClient(target, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	directory := t.TempDir()
	go func() {
		_, _, failure := endpoint.Create(ctx, directory)
		result <- failure
	}()
	await := func(signal <-chan struct{}) {
		t.Helper()
		select {
		case <-signal:
		case <-ctx.Done():
			t.Fatal("timed out waiting for shutdown stage")
		}
	}
	await(storage.entered)
	stopped := make(chan error, 1)
	go func() { stopped <- target.Shutdown(ctx) }()
	await(storage.cancelled)
	select {
	case <-storage.closed:
		t.Fatal("storage closed while operation was still running")
	case <-stopped:
		t.Fatal("shutdown returned before operation settled")
	default:
	}
	close(storage.release)
	select {
	case failure := <-result:
		if !errors.Is(failure, context.Canceled) {
			t.Fatalf("operation cancellation lost: %v", failure)
		}
	case <-ctx.Done():
		t.Fatal("shutdown lost the operation's final result")
	}
	select {
	case failure := <-stopped:
		if failure != nil {
			t.Fatal(failure)
		}
	case <-ctx.Done():
		t.Fatal("shutdown did not finish")
	}
	await(storage.closed)
}
