package sessionactor_test

import (
	"context"
	"errors"
	sessionregistry "github.com/gorenx/goren/session/registry"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	"github.com/gorenx/goren/session/persistence"
	goakt "github.com/tochemey/goakt/v4/actor"
)

type observedStorage struct {
	sessionactor.RecoveryStorage
	reads  atomic.Int32
	writes atomic.Int32
}

func (backend *observedStorage) Load(ctx context.Context, identifier session.SessionID) (*persistence.Log, error) {
	backend.reads.Add(1)
	return backend.RecoveryStorage.Load(ctx, identifier)
}

func (backend *observedStorage) AppendBatch(ctx context.Context, batch persistence.EventBatch) error {
	backend.writes.Add(1)
	return backend.RecoveryStorage.AppendBatch(ctx, batch)
}

func TestInitializationRequiresReservationBeforeStorageAccess(t *testing.T) {
	driver, replies := startSession(t, "authorized")
	backend := &observedStorage{
		RecoveryStorage: newStorage(t),
	}
	for _, input := range []any{
		sessionactor.Initialize{
			CreationID: "unreserved",
			Storage:    backend,
		},
		sessionactor.Resume{
			CreationID: "unreserved",
			Storage:    backend,
		},
	} {
		denied := request[sessionactor.Initialized](t, driver, replies, input)
		if denied.Err == nil {
			t.Fatal("unreserved creation was authorized")
		}
	}
	if backend.reads.Load() != 0 || backend.writes.Load() != 0 {
		t.Fatal("unauthorized initialization accessed storage")
	}
	initialized := request[sessionactor.Initialized](t, driver, replies, sessionactor.Initialize{
		CreationID: "authorized",
		Storage:    backend,
	})
	if initialized.Err != nil {
		t.Fatal(initialized.Err)
	}
}

func TestReactivationCannotReuseClaimAndNewCreationCanOpen(t *testing.T) {
	driver, replies := startSession(t, "creation")
	system := driver.ActorSystem()
	ctx := context.Background()
	identity, err := goakt.GrainOf[*sessionactor.Grain](ctx, system, "conversation", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	backend := &observedStorage{
		RecoveryStorage: newStorage(t),
	}
	initialized := request[sessionactor.Initialized](t, driver, replies, sessionactor.Initialize{
		CreationID: "creation",
		Storage:    backend,
	})
	if initialized.Err != nil {
		t.Fatal(initialized.Err)
	}
	closed := request[sessionactor.Closed](t, driver, replies, sessionactor.Close{
		CreationID: "creation",
	})
	if closed.Err != nil {
		t.Fatal(closed.Err)
	}
	// This test explicitly completes business close before framework deactivation.
	// It does not equate forced deactivation with completion of storage tasks.
	if err := system.TellGrain(ctx, identity, new(goakt.PoisonPill)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		present := false
		for _, active := range system.Grains(ctx, time.Second) {
			if active.Equal(identity) {
				present = true
			}
		}
		if !present {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Grain did not deactivate")
		}
		time.Sleep(time.Millisecond)
	}
	for _, input := range []any{
		sessionactor.Initialize{
			CreationID: "creation",
			Storage:    backend,
		},
		sessionactor.Resume{
			CreationID: "creation",
			Storage:    backend,
		},
	} {
		stale := request[sessionactor.Initialized](t, driver, replies, input)
		if stale.Err == nil {
			t.Fatal("reactivated instance reused old authorization")
		}
	}
	if backend.reads.Load() != 0 || backend.writes.Load() != 0 {
		t.Fatal("reactivation accessed storage without authorization")
	}
	unavailable := request[sessionactor.View](t, driver, replies, sessionactor.Read{
		CreationID: "creation",
	})
	if !errors.Is(unavailable.Err, sessionactor.ErrUnavailable) {
		t.Fatal(unavailable.Err)
	}
	registry, err := goakt.GrainOf[*sessionregistry.RegistryGrain](ctx, system, sessionregistry.RegistryName, goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []any{
		sessionregistry.Retire{
			SessionID:  "conversation",
			CreationID: "creation",
		},
		sessionregistry.Release{
			SessionID:  "conversation",
			CreationID: "creation",
		},
		sessionregistry.Reserve{
			SessionID:  "conversation",
			CreationID: "replacement",
		},
	} {
		result, err := system.AskGrain(ctx, registry, input, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if failure := result.(sessionregistry.Completion).Err; failure != nil {
			t.Fatal(failure)
		}
	}
	replacement := request[sessionactor.Initialized](t, driver, replies, sessionactor.Initialize{
		CreationID: "replacement",
		Storage:    backend,
	})
	if replacement.Err != nil {
		t.Fatal(replacement.Err)
	}
	stale := request[sessionactor.Committed](t, driver, replies, sessionactor.Commit{
		CreationID: "creation",
		Plan:       session.Batch(),
	})
	if !errors.Is(stale.Err, sessionactor.ErrCreation) {
		t.Fatal("old creation changed replacement")
	}
}
