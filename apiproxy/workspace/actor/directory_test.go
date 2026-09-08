package workspaceadapter_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gorenx/goren/apiproxy/rpc"
	"github.com/gorenx/goren/apiproxy/stream"
	workspaceapi "github.com/gorenx/goren/apiproxy/workspace"
	workspaceadapter "github.com/gorenx/goren/apiproxy/workspace/actor"
	"github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/workspace"
	workspaceactor "github.com/gorenx/goren/workspace/actor"
	"github.com/gorenx/goren/workspace/persistence/sqlite"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
)

func TestConcurrentRenameHasOneOwner(t *testing.T) {
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
		SessionHeads: emptyHeaders{},
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
	broker, err := stream.NewBroker(stream.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close()
	gateway, err := workspaceapi.NewGateway(directory, broker)
	if err != nil {
		t.Fatal(err)
	}
	methods := rpc.NewCatalog()
	if err := workspaceapi.Register(methods, gateway); err != nil {
		t.Fatal(err)
	}
	var identifiers []workspace.ID
	for range 2 {
		snapshot, _, err := directory.Create(ctx, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		identifiers = append(identifiers, snapshot.ID)
	}
	// Two independent HTTP calls race for one title. Only the business owner
	// may decide which mutation commits, regardless of Gateway concurrency.
	results := make(chan connection.RPCResult, 2)
	var group sync.WaitGroup
	for _, identifier := range identifiers {
		group.Go(func() {
			raw, _ := json.Marshal(map[string]string{
				"workspaceId": string(identifier),
				"title":       "  Shared  ",
			})
			result, err := methods.DispatchUnary(ctx, workspaceapi.RenameMethod, connection.RPCID(identifier), raw)
			if err != nil {
				t.Error(err)
				return
			}
			results <- result
		})
	}
	group.Wait()
	close(results)
	accepted, rejected := 0, 0
	for result := range results {
		if result.OK {
			accepted++
			var value workspaceapi.RenameValue
			if err := json.Unmarshal(result.Value, &value); err != nil {
				t.Fatal(err)
			}
			if value.Workspace.Title != "Shared" {
				t.Fatalf("untrimmed title: %q", value.Workspace.Title)
			}
		} else {
			rejected++
			if result.Error == nil || result.Error.Code != connection.ErrorWorkspaceNameConflict {
				t.Fatalf("unexpected conflict: %+v", result)
			}
		}
	}
	if accepted != 1 || rejected != 1 {
		t.Fatalf("accepted=%d rejected=%d", accepted, rejected)
	}
	listing, err := directory.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	matches := 0
	for _, snapshot := range listing.Workspaces {
		if snapshot.Title == "Shared" {
			matches++
		}
	}
	if matches != 1 {
		t.Fatalf("conflicting titles persisted: %+v", listing)
	}
}

// emptyHeaders describes a fresh installation with no Session records.
type emptyHeaders struct{}

func (emptyHeaders) Get(context.Context, session.SessionID) (session.Header, bool, error) {
	return session.Header{}, false, nil
}
func (emptyHeaders) List(context.Context) ([]session.Header, error) { return nil, nil }
