package workspaceadapter_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorenx/goren/apiproxy/stream"
	workspaceapi "github.com/gorenx/goren/apiproxy/workspace"
	workspaceadapter "github.com/gorenx/goren/apiproxy/workspace/actor"
	"github.com/gorenx/goren/workspace"
	workspaceactor "github.com/gorenx/goren/workspace/actor"
	"github.com/gorenx/goren/workspace/persistence/sqlite"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
)

func TestWorkspaceCommitsReachHostFeedThroughPubSub(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	system, err := goakt.NewActorSystem("workspace-feed", goakt.WithPubSub(), goakt.WithLogger(log.DiscardLogger))
	if err != nil {
		t.Fatal(err)
	}
	err = system.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer system.Stop(context.Background())
	storage, err := sqlite.Open(ctx, sqlite.Config{
		Path:        filepath.Join(t.TempDir(), "workspace.sqlite"),
		JournalMode: sqlite.JournalWAL,
	})
	if err != nil {
		t.Fatal(err)
	}
	target, err := workspaceactor.Spawn(ctx, system, "workspace", storage, workspace.RegistryOptions{
		SessionHeads: emptyHeaders{},
	})
	if err != nil {
		_ = storage.Close(context.Background())
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
	broker, err := stream.NewBroker(stream.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close()
	gateway, err := workspaceapi.NewGateway(directory, broker)
	if err != nil {
		t.Fatal(err)
	}
	subscriber, err := workspaceadapter.SpawnSubscriber(ctx, system, "workspace-events", gateway)
	if err != nil {
		t.Fatal(err)
	}
	defer subscriber.Shutdown(context.Background())
	feed, err := broker.OpenHost()
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Release(feed)
	read := func(kind string) map[string]json.RawMessage {
		t.Helper()
		batch, present, failure := feed.Next(ctx)
		if failure != nil || !present || len(batch) != 1 {
			t.Fatalf("host frame missing: %v %v %v", batch, present, failure)
		}
		var value map[string]json.RawMessage
		failure = json.Unmarshal(batch[0].Payload, &value)
		if failure != nil {
			t.Fatal(failure)
		}
		var actual string
		failure = json.Unmarshal(value["type"], &actual)
		if failure != nil || actual != kind {
			t.Fatalf("frame type = %q, want %q: %s", actual, kind, batch[0].Payload)
		}
		return value
	}
	created, fresh, err := directory.Create(ctx, t.TempDir())
	if err != nil || !fresh {
		t.Fatalf("create: %v, %v", fresh, err)
	}
	frame := read("host/workspace-changed")
	var state workspaceapi.View
	err = json.Unmarshal(frame["workspace"], &state)
	if err != nil || string(state.WorkspaceID) != string(created.ID) {
		t.Fatalf("created frame: %s, %v", frame["workspace"], err)
	}
	_, err = directory.Rename(ctx, created.ID, "Renamed")
	if err != nil {
		t.Fatal(err)
	}
	frame = read("host/workspace-changed")
	err = json.Unmarshal(frame["workspace"], &state)
	if err != nil || state.Title != "Renamed" {
		t.Fatalf("renamed frame: %s, %v", frame["workspace"], err)
	}
	second, _, err := directory.Create(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	read("host/workspace-changed")
	_, err = directory.InsertBefore(ctx, created.ID, &second.ID)
	if err != nil {
		t.Fatal(err)
	}
	read("host/workspace-order-changed")
	removed, err := directory.Delete(ctx, created.ID)
	if err != nil || !removed {
		t.Fatalf("delete: %v, %v", removed, err)
	}
	read("host/workspace-removed")
	err = subscriber.Shutdown(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = directory.Create(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	wait, stop := context.WithTimeout(ctx, 50*time.Millisecond)
	defer stop()
	_, _, err = feed.Next(wait)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stopped subscriber forwarded a new commit: %v", err)
	}
}

// discardedNotices is only used to validate startup rejection, never as a
// replacement for the production Gateway in notification integration tests.
type discardedNotices struct{}

func (discardedNotices) ObserveEvent(context.Context, workspace.Notice) error { return nil }

func TestWorkspaceSubscriberRejectsMissingPubSubAndCanceledStartup(t *testing.T) {
	system, err := goakt.NewActorSystem("workspace-no-pubsub", goakt.WithLogger(log.DiscardLogger))
	if err != nil {
		t.Fatal(err)
	}
	err = system.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer system.Stop(context.Background())
	endpoint, err := workspaceadapter.SpawnSubscriber(context.Background(), system, "events", discardedNotices{})
	if err == nil || endpoint != nil {
		t.Fatalf("startup without PubSub: %v %v", endpoint, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	endpoint, err = workspaceadapter.SpawnSubscriber(ctx, system, "events", discardedNotices{})
	if !errors.Is(err, context.Canceled) || endpoint != nil {
		t.Fatalf("canceled startup: %v %v", endpoint, err)
	}
}
