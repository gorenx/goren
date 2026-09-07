package persistenceactor_test

import (
	"context"
	"errors"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/session/persistence"
	persistenceactor "github.com/gorenx/goren/session/persistence/actor"
	"github.com/gorenx/goren/session/persistence/sqlite"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
	"github.com/tochemey/goakt/v4/reentrancy"
	"path/filepath"
	"testing"
	"time"
)

type command struct {
	payload any
}

type response struct {
	value any
	err   error
}

type requester struct {
	target  *goakt.PID
	replies chan response
}

func (*requester) PreStart(*goakt.Context) error { return nil }
func (*requester) PostStop(*goakt.Context) error { return nil }
func (driver *requester) Receive(rc *goakt.ReceiveContext) {
	if input, ok := rc.Message().(command); ok {
		replies := driver.replies
		rc.Request(driver.target, input.payload).Then(func(value any, err error) {
			replies <- response{
				value: value,
				err:   err,
			}
		})
	}
}

func request[T any](t *testing.T, driver *goakt.PID, replies <-chan response, payload any) T {
	t.Helper()
	if err := goakt.Tell(context.Background(), driver, command{
		payload: payload,
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-replies:
		if result.err != nil {
			t.Fatal(result.err)
		}
		value, ok := result.value.(T)
		if !ok {
			t.Fatalf("unexpected reply %T", result.value)
		}
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("request timed out")
		var empty T
		return empty
	}
}

func TestDurableReadsThroughActorRequests(t *testing.T) {
	ctx := context.Background()
	backend, err := sqlite.Open(ctx, sqlite.Config{
		Path:        filepath.Join(t.TempDir(), "session.sqlite"),
		JournalMode: sqlite.JournalWAL,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := backend.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	conversation, err := session.NewLog("conversation", session.CreateOptions{})
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
	owner, err := persistenceactor.New(backend)
	if err != nil {
		t.Fatal(err)
	}
	system, err := goakt.NewActorSystem("durable-reads", goakt.WithLogger(log.DiscardLogger))
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
	target, err := system.Spawn(ctx, "persistence", owner)
	if err != nil {
		t.Fatal(err)
	}
	replies := make(chan response, 4)
	driver, err := system.Spawn(ctx, "requester", &requester{
		target:  target,
		replies: replies,
	}, goakt.WithReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		t.Fatal(err)
	}
	inspected := request[persistenceactor.Inspection](t, driver, replies, persistenceactor.Inspect{
		SessionID: "conversation",
	})
	if inspected.Err != nil || inspected.Value.Header.ID != "conversation" {
		t.Fatalf("inspect = %#v", inspected)
	}
	listed := request[persistenceactor.Snapshots](t, driver, replies, persistenceactor.ListSnapshots{
		Page: persistence.SessionPage{
			Limit: 10,
		},
	})
	if listed.Err != nil || len(listed.Value.Snapshots) != 1 {
		t.Fatalf("list = %#v", listed)
	}
	segment := request[persistenceactor.Segment](t, driver, replies, persistenceactor.ReadEventsFrom{
		SessionID: "conversation",
		Page: persistence.EventContinuation{
			FromSeq: 0,
			Limit:   10,
		},
	})
	if segment.Err != nil || len(segment.Value.Events) != len(conversation.Events()) {
		t.Fatalf("segment = %#v", segment)
	}
	window := request[persistenceactor.Window](t, driver, replies, persistenceactor.ReadEventsBefore{
		SessionID: "conversation",
		Page: persistence.EventPage{
			Limit: 10,
		},
	})
	if window.Err != nil || len(window.Value.Events) != len(conversation.Events()) {
		t.Fatalf("window = %#v", window)
	}
	absent := request[persistenceactor.Inspection](t, driver, replies, persistenceactor.Inspect{
		SessionID: "missing",
	})
	var missing *persistence.NotFoundError
	if !errors.As(absent.Err, &missing) {
		t.Fatalf("missing = %v", absent.Err)
	}
	if err := system.Kill(ctx, "persistence"); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Load(ctx, "conversation"); err != nil {
		t.Fatalf("Actor closed shared storage: %v", err)
	}
}
