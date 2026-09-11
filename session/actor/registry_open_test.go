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

func TestRegistryCreateFixesInputAndClosesBeforeReplacement(t *testing.T) {
	backend := newStorage(t)
	system := startSystem(t)
	driver, replies := registryRequester(t, system)
	cwd := t.TempDir()
	createdAt := int64(123)
	input := sessionregistry.CreateSession{
		SessionID:  "conversation",
		CreationID: "first",
		Options: session.CreateOptions{
			Seed: []session.Event{},
			Metadata: session.Metadata{
				CreatedAt: &createdAt,
				CWD:       &cwd,
			},
		},
		Storage: backend,
	}
	empty := request[sessionregistry.Sessions](t, driver, replies, sessionregistry.ListSessions{})
	if len(empty.Items) != 0 {
		t.Fatalf("initial list = %#v", empty)
	}
	initialized := request[sessionactor.Initialized](t, driver, replies, input)
	if initialized.Err != nil || initialized.Header.CreatedAt != createdAt || initialized.Barrier.NextSeq != 1 {
		t.Fatalf("create = %#v", initialized)
	}
	listed := request[sessionregistry.Sessions](t, driver, replies, sessionregistry.ListSessions{})
	if len(listed.Items) != 1 || listed.Items[0].CreationID != "first" || *listed.Items[0].Header.CWD != cwd {
		t.Fatalf("initialized list = %#v", listed)
	}
	*listed.Items[0].Header.CWD = "changed listing"
	listed = request[sessionregistry.Sessions](t, driver, replies, sessionregistry.ListSessions{})
	if *listed.Items[0].Header.CWD != cwd {
		t.Fatal("listing exposed retained header")
	}
	*initialized.Header.CWD = "changed reply"
	repeated := request[sessionactor.Initialized](t, driver, replies, input)
	if repeated.Err != nil || *repeated.Header.CWD != cwd {
		t.Fatalf("repeat = %#v", repeated)
	}
	// Mutating caller-owned input after the response cannot change the retained input.
	createdAt = 456
	conflicting := request[sessionactor.Initialized](t, driver, replies, input)
	if conflicting.Err == nil {
		t.Fatal("creation input changed on retry")
	}
	createdAt = 123
	restoredInput := request[sessionactor.Initialized](t, driver, replies, input)
	if restoredInput.Err != nil || restoredInput.Header.CreatedAt != 123 {
		t.Fatal("registry retained caller pointer")
	}
	modeChange := request[sessionactor.Initialized](t, driver, replies, sessionregistry.ResumeSession{
		SessionID:  "conversation",
		CreationID: "first",
		Storage:    backend,
	})
	if modeChange.Err == nil {
		t.Fatal("creation switched to recovery")
	}
	closed := request[sessionactor.Closed](t, driver, replies, sessionregistry.CloseSession{
		SessionID:  "conversation",
		CreationID: "first",
	})
	if closed.Err != nil || closed.Barrier.NextSeq != 1 {
		t.Fatalf("close = %#v", closed)
	}
	listed = request[sessionregistry.Sessions](t, driver, replies, sessionregistry.ListSessions{})
	if len(listed.Items) != 0 {
		t.Fatalf("closed Session remained listed: %#v", listed)
	}
	resumed := request[sessionactor.Initialized](t, driver, replies, sessionregistry.ResumeSession{
		SessionID:  "conversation",
		CreationID: "second",
		Storage:    backend,
	})
	if resumed.Err != nil || resumed.Header.CreatedAt != 123 || resumed.Barrier.NextSeq != 1 {
		t.Fatalf("replacement recovery = %#v", resumed)
	}
	listed = request[sessionregistry.Sessions](t, driver, replies, sessionregistry.ListSessions{})
	if len(listed.Items) != 1 || listed.Items[0].CreationID != "second" || listed.Items[0].Header.CreatedAt != 123 {
		t.Fatalf("resumed list = %#v", listed)
	}
	final := request[sessionactor.Closed](t, driver, replies, sessionregistry.CloseSession{
		SessionID:  "conversation",
		CreationID: "second",
	})
	if final.Err != nil {
		t.Fatal(final.Err)
	}
}

type gatedRecovery struct {
	sessionactor.RecoveryStorage
	reads   atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func (backend *gatedRecovery) Load(ctx context.Context, identifier session.SessionID) (*persistence.Log, error) {
	if backend.reads.Add(1) == 1 {
		close(backend.entered)
		select {
		case <-backend.release:
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		}
	}
	return backend.RecoveryStorage.Load(ctx, identifier)
}

func TestRegistrySharesRecoveryAndKeepsIdentityDuringInitialization(t *testing.T) {
	backend := newStorage(t)
	history, err := session.NewLog("conversation", session.CreateOptions{
		Seed: []session.Event{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := backend.AppendBatch(context.Background(), persistence.EventBatch{
		Header:      history.Header(),
		Events:      history.Events(),
		Materialize: true,
	}); err != nil {
		t.Fatal(err)
	}
	system := startSystem(t)
	driver, replies := registryRequester(t, system)
	storage := &gatedRecovery{
		RecoveryStorage: backend,
		entered:         make(chan struct{}),
		release:         make(chan struct{}),
	}
	defer releaseGate(storage.release)
	input := sessionregistry.ResumeSession{
		SessionID:  "conversation",
		CreationID: "resume",
		Storage:    storage,
	}
	if err := goakt.Tell(context.Background(), driver, command{
		payload: input,
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-storage.entered:
	case <-time.After(time.Second):
		t.Fatal("recovery did not start")
	}
	listed := request[sessionregistry.Sessions](t, driver, replies, sessionregistry.ListSessions{})
	if len(listed.Items) != 0 {
		t.Fatalf("pending recovery appeared in list: %#v", listed)
	}
	if err := goakt.Tell(context.Background(), driver, command{
		payload: input,
	}); err != nil {
		t.Fatal(err)
	}
	if err := goakt.Tell(context.Background(), driver, command{
		payload: sessionregistry.CloseSession{
			SessionID:  "conversation",
			CreationID: "resume",
		},
	}); err != nil {
		t.Fatal(err)
	}
	conflicting := request[sessionactor.Initialized](t, driver, replies, sessionregistry.CreateSession{
		SessionID:  "conversation",
		CreationID: "replacement",
		Storage:    backend,
	})
	if conflicting.Err == nil {
		t.Fatal("initializing identity was replaced")
	}
	if storage.reads.Load() != 1 {
		t.Fatal("duplicate recovery accessed storage")
	}
	select {
	case result := <-replies:
		t.Fatalf("operation replied before recovery settled: %#v", result)
	default:
	}
	close(storage.release)
	initializedCount := 0
	closedCount := 0
	for index := 0; index < 3; index++ {
		select {
		case result := <-replies:
			if result.err != nil {
				t.Fatal(result.err)
			}
			switch value := result.value.(type) {
			case sessionactor.Initialized:
				if value.Err != nil {
					t.Fatal(value.Err)
				}
				initializedCount++
			case sessionactor.Closed:
				if value.Err != nil || value.Barrier.NextSeq != 1 {
					t.Fatalf("automatic close = %#v", value)
				}
				closedCount++
			default:
				t.Fatalf("unexpected result: %#v", result)
			}
		case <-time.After(time.Second):
			t.Fatal("initialization or automatic close did not finish")
		}
	}
	if initializedCount != 2 || closedCount != 1 {
		t.Fatal("missing initialization or automatic close result")
	}

	if storage.reads.Load() != 1 {
		t.Fatal("duplicate recovery executed")
	}
	closed := request[sessionactor.Closed](t, driver, replies, sessionregistry.CloseSession{
		SessionID:  "conversation",
		CreationID: "resume",
	})
	if closed.Err != nil {
		t.Fatal(closed.Err)
	}
}

func TestRegistryAutomaticallyClosesWhenPendingRecoveryFails(t *testing.T) {
	backend := newStorage(t)
	system := startSystem(t)
	driver, replies := registryRequester(t, system)
	storage := &gatedRecovery{
		RecoveryStorage: backend,
		entered:         make(chan struct{}),
		release:         make(chan struct{}),
	}
	defer releaseGate(storage.release)
	if err := goakt.Tell(context.Background(), driver, command{
		payload: sessionregistry.ResumeSession{
			SessionID:  "conversation",
			CreationID: "missing",
			Storage:    storage,
		},
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-storage.entered:
	case <-time.After(time.Second):
		t.Fatal("recovery did not start")
	}
	if err := goakt.Tell(context.Background(), driver, command{
		payload: sessionregistry.CloseSession{
			SessionID:  "conversation",
			CreationID: "missing",
		},
	}); err != nil {
		t.Fatal(err)
	}
	// The registry must retain the creation while the failed load is unresolved.
	refused := request[sessionregistry.Completion](t, driver, replies, sessionregistry.Reserve{
		SessionID:  "conversation",
		CreationID: "replacement",
	})
	if refused.Err == nil {
		t.Fatal("unresolved recovery released its reservation")
	}
	close(storage.release)
	initializedCount := 0
	closedCount := 0
	for index := 0; index < 2; index++ {
		select {
		case result := <-replies:
			if result.err != nil {
				t.Fatal(result.err)
			}
			switch value := result.value.(type) {
			case sessionactor.Initialized:
				var missing *persistence.NotFoundError
				if !errors.As(value.Err, &missing) {
					t.Fatalf("recovery = %#v", value)
				}
				initializedCount++
			case sessionactor.Closed:
				if value.Err != nil || value.Barrier.NextSeq != 0 {
					t.Fatalf("cleanup = %#v", value)
				}
				closedCount++
			default:
				t.Fatalf("unexpected reply: %#v", result)
			}
		case <-time.After(time.Second):
			t.Fatal("failed recovery cleanup did not complete")
		}
	}
	if initializedCount != 1 || closedCount != 1 {
		t.Fatal("missing recovery or cleanup result")
	}
	created := request[sessionactor.Initialized](t, driver, replies, sessionregistry.CreateSession{
		SessionID:  "conversation",
		CreationID: "replacement",
		Storage:    backend,
	})
	if created.Err != nil {
		t.Fatal("failed recovery leaked its reservation", created.Err)
	}
}
