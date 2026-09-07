package sessionactor_test

import (
	"context"
	"errors"
	sessionregistry "github.com/gorenx/goren/session/registry"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	"github.com/gorenx/goren/session/persistence/sqlite"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
	"github.com/tochemey/goakt/v4/reentrancy"
)

type command struct {
	payload any
}

type response struct {
	value any
	err   error
}

type requester struct {
	target  *goakt.GrainIdentity
	replies chan response
}

func (*requester) PreStart(*goakt.Context) error { return nil }
func (*requester) PostStop(*goakt.Context) error { return nil }
func (driver *requester) Receive(rc *goakt.ReceiveContext) {
	if incoming, ok := rc.Message().(command); ok {
		switch input := incoming.payload.(type) {
		case sessionactor.Initialize:
			authority, err := goakt.GrainOf[*sessionregistry.RegistryGrain](rc.Context(), rc.ActorSystem(), sessionregistry.RegistryName, goakt.WithLongLivedGrain())
			if err != nil {
				driver.replies <- response{
					err: err,
				}
				return
			}
			input.Authority = authority
			incoming.payload = input
		case sessionactor.Resume:
			authority, err := goakt.GrainOf[*sessionregistry.RegistryGrain](rc.Context(), rc.ActorSystem(), sessionregistry.RegistryName, goakt.WithLongLivedGrain())
			if err != nil {
				driver.replies <- response{
					err: err,
				}
				return
			}
			input.Authority = authority
			incoming.payload = input
		case sessionactor.Close:
			authority, err := goakt.GrainOf[*sessionregistry.RegistryGrain](rc.Context(), rc.ActorSystem(), sessionregistry.RegistryName, goakt.WithLongLivedGrain())
			if err != nil {
				driver.replies <- response{
					err: err,
				}
				return
			}
			input.Authority = authority
			incoming.payload = input
		}
		pending := rc.RequestGrain(driver.target, incoming.payload)
		if pending == nil {
			driver.replies <- response{
				err: errors.New("test request was not admitted"),
			}
			return
		}
		replies := driver.replies
		pending.Then(func(value any, err error) {
			replies <- response{
				value: value,
				err:   err,
			}
		})
	}
}

func startSystem(t *testing.T, options ...goakt.Option) goakt.ActorSystem {
	t.Helper()
	system, err := goakt.NewActorSystem("session-test", append([]goakt.Option{goakt.WithLogger(log.DiscardLogger)}, options...)...)
	if err != nil {
		t.Fatal(err)
	}
	if err := system.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := system.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	return system
}

func startSession(t *testing.T, creationID string) (*goakt.PID, <-chan response) {
	t.Helper()
	system := startSystem(t)
	registry, err := goakt.GrainOf[*sessionregistry.RegistryGrain](context.Background(), system, sessionregistry.RegistryName, goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	result, err := system.AskGrain(context.Background(), registry, sessionregistry.Reserve{
		SessionID:  "conversation",
		CreationID: creationID,
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if failure := result.(sessionregistry.Completion).Err; failure != nil {
		t.Fatal(failure)
	}
	identity, err := goakt.GrainOf[*sessionactor.Grain](context.Background(), system, "conversation", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	replies := make(chan response, 1)
	driver, err := system.Spawn(context.Background(), "requester", &requester{
		target:  identity,
		replies: replies,
	}, goakt.WithReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		t.Fatal(err)
	}
	return driver, replies
}

func request[T any](t *testing.T, driver *goakt.PID, replies <-chan response, input any) T {
	t.Helper()
	if err := goakt.Tell(context.Background(), driver, command{
		payload: input,
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
		t.Fatal("Session request timed out")
		var empty T
		return empty
	}
}

func TestSessionGrainCommitsAndReadsDetachedSnapshots(t *testing.T) {
	driver, replies := startSession(t, "creation")
	unavailable := request[sessionactor.View](t, driver, replies, sessionactor.Read{
		CreationID: "creation",
	})
	if !errors.Is(unavailable.Err, sessionactor.ErrUnavailable) {
		t.Fatalf("uninitialized = %#v", unavailable)
	}
	initialized := request[sessionactor.Initialized](t, driver, replies, sessionactor.Initialize{
		Storage:    newStorage(t),
		CreationID: "creation",
	})
	if initialized.Err != nil || initialized.Header.ID != "conversation" || initialized.Barrier.NextSeq != 0 {
		t.Fatalf("initialize = %#v", initialized)
	}
	draft, err := session.NewEventDraft(session.TurnStarted, session.TurnStart{
		Turn: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	committed := request[sessionactor.Committed](t, driver, replies, sessionactor.Commit{
		CreationID: "creation",
		Plan:       session.Batch(draft),
	})
	if committed.Err != nil || committed.Result.FirstSeq != 0 || committed.Result.NextSeq != 1 {
		t.Fatalf("commit = %#v", committed)
	}
	// A returned event cannot mutate the owned log or another read result.
	committed.Result.Events[0].Data[0] = '!'
	view := request[sessionactor.View](t, driver, replies, sessionactor.Read{
		CreationID: "creation",
		Minimum: &session.WriteBarrier{
			SessionID: "conversation",
			NextSeq:   1,
		},
	})
	if view.Err != nil || len(view.Snapshot.Events) != 1 || string(view.Snapshot.Events[0].Data) != `{"turn":1}` {
		t.Fatalf("view = %#v", view)
	}
	view.Snapshot.Events[0].Data[0] = '!'
	repeated := request[sessionactor.Initialized](t, driver, replies, sessionactor.Initialize{
		CreationID: "creation",
	})
	if repeated.Err != nil || repeated.Barrier.NextSeq != 1 {
		t.Fatalf("reinitialization reset log: %#v", repeated)
	}
	failed := request[sessionactor.Committed](t, driver, replies, sessionactor.Commit{
		CreationID: "creation",
		Plan:       session.Batch(draft, session.EventDraft{}),
	})
	if failed.Err == nil || failed.Result.NextSeq != 1 {
		t.Fatalf("partial batch committed: %#v", failed)
	}
	final := request[sessionactor.View](t, driver, replies, sessionactor.Read{
		CreationID: "creation",
	})
	if final.Err != nil || final.Snapshot.Barrier.NextSeq != 1 || string(final.Snapshot.Events[0].Data) != `{"turn":1}` {
		t.Fatalf("final view = %#v", final)
	}
}

func TestSessionGrainRejectsForeignCreationAndUnavailablePrefix(t *testing.T) {
	driver, replies := startSession(t, "current")
	initialized := request[sessionactor.Initialized](t, driver, replies, sessionactor.Initialize{
		Storage:    newStorage(t),
		CreationID: "current",
	})
	if initialized.Err != nil {
		t.Fatal(initialized.Err)
	}
	foreign := request[sessionactor.Initialized](t, driver, replies, sessionactor.Initialize{
		CreationID: "old",
	})
	if !errors.Is(foreign.Err, sessionactor.ErrCreation) {
		t.Fatal("foreign creation replaced Session")
	}
	for _, identifier := range []string{"", "old"} {
		result := request[sessionactor.Committed](t, driver, replies, sessionactor.Commit{
			CreationID: identifier,
			Plan:       session.Batch(),
		})
		if !errors.Is(result.Err, sessionactor.ErrCreation) {
			t.Fatalf("foreign commit = %#v", result)
		}
	}
	for _, barrier := range []session.WriteBarrier{
		{
			SessionID: "other",
		},
		{
			SessionID: "conversation",
			NextSeq:   -1,
		},
		{
			SessionID: "conversation",
			NextSeq:   1,
		},
	} {
		result := request[sessionactor.View](t, driver, replies, sessionactor.Read{
			CreationID: "current",
			Minimum:    &barrier,
		})
		if !errors.Is(result.Err, sessionactor.ErrBarrier) {
			t.Fatalf("invalid barrier = %#v", result)
		}
	}
}

func newStorage(t *testing.T) *sqlite.Adapter {
	t.Helper()
	backend, err := sqlite.Open(context.Background(), sqlite.Config{
		Path:        filepath.Join(t.TempDir(), "sessions.sqlite"),
		JournalMode: sqlite.JournalWAL,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := backend.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return backend
}
