package sessionactor_test

import (
	"context"
	"errors"
	sessionregistry "github.com/gorenx/goren/session/registry"
	"testing"
	"time"

	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

func registryRequester(t *testing.T, system goakt.ActorSystem) (*goakt.PID, <-chan response) {
	t.Helper()
	registry, err := goakt.GrainOf[*sessionregistry.RegistryGrain](context.Background(), system, sessionregistry.RegistryName, goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	replies := make(chan response, 8)
	driver, err := system.Spawn(context.Background(), "registry-requester", &requester{
		target:  registry,
		replies: replies,
	}, goakt.WithReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		t.Fatal(err)
	}
	return driver, replies
}

func TestRegistryCloseWaitsForDurabilityAndDeactivationBeforeRelease(t *testing.T) {
	backend := newStorage(t)
	driver, replies := startSession(t, "creation")
	coordinator, completions := registryRequester(t, driver.ActorSystem())
	storage := &gatedStorage{
		Storage: backend,
		entered: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	defer releaseGate(storage.release)
	initializeAndCommit(t, driver, replies, storage)
	select {
	case <-storage.entered:
	case <-time.After(time.Second):
		t.Fatal("write did not start")
	}
	for index := 0; index < 2; index++ {
		if err := goakt.Tell(context.Background(), coordinator, command{
			payload: sessionregistry.CloseSession{
				SessionID:  "conversation",
				CreationID: "creation",
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	// A second request to the registry proves its mailbox remains available
	// while Close is waiting for storage, and that replacement is denied.
	refused := request[sessionregistry.Completion](t, coordinator, completions, sessionregistry.Reserve{
		SessionID:  "conversation",
		CreationID: "replacement",
	})
	if refused.Err == nil {
		t.Fatal("replacement reserved before close finished")
	}
	close(storage.release)
	for index := 0; index < 2; index++ {
		closed := receiveClosed(t, completions)
		if closed.Err != nil || closed.Barrier.NextSeq != 1 {
			t.Fatalf("close = %#v", closed)
		}
	}
	for _, active := range driver.ActorSystem().Grains(context.Background(), time.Second) {
		if active.Name() == "conversation" {
			t.Fatal("close returned before Session deactivation")
		}
	}
	repeated := request[sessionactor.Closed](t, coordinator, completions, sessionregistry.CloseSession{
		SessionID:  "conversation",
		CreationID: "creation",
	})
	if repeated.Err != nil || repeated.Barrier.NextSeq != 1 {
		t.Fatalf("lost close result: %#v", repeated)
	}
	replayed := request[sessionregistry.Completion](t, coordinator, completions, sessionregistry.Reserve{
		SessionID:  "conversation",
		CreationID: "creation",
	})
	if replayed.Err == nil {
		t.Fatal("completed creation was reserved again")
	}
	reserved := request[sessionregistry.Completion](t, coordinator, completions, sessionregistry.Reserve{
		SessionID:  "conversation",
		CreationID: "replacement",
	})
	if reserved.Err != nil {
		t.Fatal(reserved.Err)
	}
	stale := request[sessionactor.Closed](t, coordinator, completions, sessionregistry.CloseSession{
		SessionID:  "conversation",
		CreationID: "creation",
	})
	if stale.Err == nil {
		t.Fatal("old close accepted for replacement")
	}
	stored, err := backend.Load(context.Background(), "conversation")
	if err != nil || stored == nil || len(stored.Events) != 1 {
		t.Fatal("close lost durable log", err)
	}
}

func TestRegistryCloseFailureKeepsReservationAndRetries(t *testing.T) {
	driver, replies := startSession(t, "creation")
	coordinator, completions := registryRequester(t, driver.ActorSystem())
	storage := &failingStorage{
		Storage: newStorage(t),
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	defer releaseGate(storage.release)
	initializeAndCommit(t, driver, replies, storage)
	select {
	case <-storage.entered:
	case <-time.After(time.Second):
		t.Fatal("write did not start")
	}
	if err := goakt.Tell(context.Background(), coordinator, command{
		payload: sessionregistry.CloseSession{
			SessionID:  "conversation",
			CreationID: "creation",
		},
	}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		attempted := request[sessionactor.Committed](t, driver, replies, sessionactor.Commit{
			CreationID: "creation",
			Plan:       closeProbe{},
		})
		if errors.Is(attempted.Err, session.ErrWritesClosed) {
			break
		}
		if attempted.Err != nil || time.Now().After(deadline) {
			t.Fatal("Session did not seal writes", attempted.Err)
		}
	}
	close(storage.release)
	failed := receiveClosed(t, completions)
	if !errors.Is(failed.Err, writeFailure) {
		t.Fatalf("close = %#v", failed)
	}
	denied := request[sessionregistry.Completion](t, coordinator, completions, sessionregistry.Reserve{
		SessionID:  "conversation",
		CreationID: "replacement",
	})
	if denied.Err == nil {
		t.Fatal("failed close released identity")
	}
	sealed := request[sessionactor.Committed](t, driver, replies, sessionactor.Commit{
		CreationID: "creation",
		Plan:       closeProbe{},
	})
	if !errors.Is(sealed.Err, session.ErrWritesClosed) {
		t.Fatal("failed close reopened writes")
	}
	retried := request[sessionactor.Closed](t, coordinator, completions, sessionregistry.CloseSession{
		SessionID:  "conversation",
		CreationID: "creation",
	})
	if retried.Err != nil || retried.Barrier.NextSeq != 1 {
		t.Fatalf("retry = %#v", retried)
	}
}

// closeProbe makes no changes while testing whether write admission is sealed.
type closeProbe struct{}

func (closeProbe) Build(context.Context, session.Snapshot) ([]session.EventDraft, error) {
	return nil, nil
}
