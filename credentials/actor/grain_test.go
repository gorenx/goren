package credentialactor_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorenx/goren/credentials"
	credentialactor "github.com/gorenx/goren/credentials/actor"
	credentialslocal "github.com/gorenx/goren/credentials/local"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
	"github.com/tochemey/goakt/v4/reentrancy"
)

type environment map[string]string

func (values environment) Lookup(name string) (string, bool) {
	value, found := values[name]
	return value, found
}

type response struct {
	payload any
	err     error
}

type request struct {
	target  *goakt.GrainIdentity
	message any
}

type requester struct {
	results chan response
}

func (*requester) PreStart(*goakt.Context) error { return nil }
func (*requester) PostStop(*goakt.Context) error { return nil }
func (consumer *requester) Receive(rc *goakt.ReceiveContext) {
	if incoming, ok := rc.Message().(request); ok {
		rc.RequestGrain(incoming.target, incoming.message, goakt.WithRequestTimeout(3*time.Second)).Then(func(payload any, err error) {
			consumer.results <- response{
				payload: payload,
				err:     err,
			}
		})
	}
}

type fixture struct {
	system   goakt.ActorSystem
	identity *goakt.GrainIdentity
	endpoint *credentialactor.Client
}

func start(t *testing.T, storage credentials.LiveStore, platform credentials.Environment) fixture {
	t.Helper()
	business, err := credentials.NewManager(storage, platform)
	if err != nil {
		t.Fatal(err)
	}
	running, err := goakt.NewActorSystem("credentials-test", goakt.WithLogger(log.DiscardLogger))
	if err != nil {
		t.Fatal(err)
	}
	if err = running.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := running.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	identity, err := goakt.GrainOf[*credentialactor.Grain](context.Background(), running, "credentials",
		goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	payload, err := running.AskGrain(context.Background(), identity, credentialactor.Initialize{
		Manager: business,
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if failure := payload.(credentialactor.Completion).Err; failure != nil {
		t.Fatal(failure)
	}
	endpoint, err := credentialactor.NewClient(running)
	if err != nil {
		t.Fatal(err)
	}
	return fixture{
		system:   running,
		identity: identity,
		endpoint: endpoint,
	}
}

func (setup fixture) send(t *testing.T, message any) <-chan response {
	t.Helper()
	results := make(chan response, 1)
	pid, err := setup.system.Spawn(context.Background(), uuid.NewString(), &requester{
		results: results,
	}, goakt.WithReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		t.Fatal(err)
	}
	if err := goakt.Tell(context.Background(), pid, request{
		target:  setup.identity,
		message: message,
	}); err != nil {
		t.Fatal(err)
	}
	return results
}

func receive(t *testing.T, results <-chan response) any {
	t.Helper()
	select {
	case observed := <-results:
		if observed.err != nil {
			t.Fatal(observed.err)
		}
		return observed.payload
	case <-time.After(4 * time.Second):
		t.Fatal("credential request did not finish")
		return nil
	}
}

func TestGrainPersistsAndRemovesCredentials(t *testing.T) {
	storage, err := credentialslocal.NewLiveStore(credentialslocal.Config{
		Path: filepath.Join(t.TempDir(), "credentials.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	setup := start(t, storage, environment{})
	ctx := context.Background()
	const ref string = "TEST_CREDENTIAL"
	if err := setup.endpoint.Set(ctx, ref, "fixture-value"); err != nil {
		t.Fatal(err)
	}
	info, err := setup.endpoint.Describe(ctx, ref)
	if err != nil || !info.Configured || !info.Writable || info.Source != "file" {
		t.Fatalf("metadata: %+v, %v", info, err)
	}
	resolved, err := setup.endpoint.Resolve(ctx, ref)
	if err != nil || resolved.Value != "fixture-value" {
		t.Fatal("resolution failed")
	}
	if err := setup.endpoint.Unset(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if _, err := setup.endpoint.Resolve(ctx, ref); !errors.Is(err, credentials.ErrNotFound) {
		t.Fatalf("removal: %v", err)
	}
}

type storageGate struct {
	values  map[string]string
	entered chan struct{}
	release chan struct{}
	failure error
}

func (*storageGate) Source() string { return "fixture" }
func (storage *storageGate) Load(ctx context.Context, ref string) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	if storage.failure != nil {
		return "", false, storage.failure
	}
	value, found := storage.values[ref]
	return value, found, nil
}
func (storage *storageGate) Save(ctx context.Context, ref string, value string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if storage.entered != nil {
		storage.entered <- struct{}{}
		<-storage.release // Deliberately non-interruptible once admitted.
	}
	storage.values[ref] = value
	return nil
}
func (storage *storageGate) Delete(ctx context.Context, ref string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	delete(storage.values, ref)
	return nil
}

func TestGrainMailboxKeepsReadBehindWrite(t *testing.T) {
	storage := &storageGate{
		values:  make(map[string]string),
		entered: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	setup := start(t, storage, environment{})
	unlock := sync.OnceFunc(func() { close(storage.release) })
	t.Cleanup(unlock)
	write := setup.send(t, credentialactor.Set{
		Context: context.Background(),
		Ref:     "TEST_CREDENTIAL",
		Value:   "committed",
	})
	select {
	case <-storage.entered:
	case <-time.After(time.Second):
		t.Fatal("storage not entered")
	}
	read := setup.send(t, credentialactor.Resolve{
		Context: context.Background(),
		Ref:     "TEST_CREDENTIAL",
	})
	unlock()
	if result := receive(t, write).(credentialactor.Completion); result.Err != nil {
		t.Fatal(result.Err)
	}
	resolved := receive(t, read).(credentialactor.Resolution)
	if resolved.Err != nil || resolved.Resolved.Value != "committed" {
		t.Fatal("read overtook write")
	}
}

func TestGrainPreservesBusinessErrorsAndEnvironmentRules(t *testing.T) {
	cause := errors.New("storage unavailable")
	storage := &storageGate{
		values:  make(map[string]string),
		failure: fmt.Errorf("load: %w", cause),
	}
	setup := start(t, storage, environment{
		"ENV_CREDENTIAL": "environment-value",
	})
	ctx := context.Background()
	_, err := setup.endpoint.Resolve(ctx, "STORED_CREDENTIAL")
	if !errors.Is(err, cause) {
		t.Fatalf("lost business error: %v", err)
	}
	resolved, err := setup.endpoint.Resolve(ctx, "ENV_CREDENTIAL")
	if err != nil || resolved.Source != "env" {
		t.Fatal("environment precedence lost")
	}
	if err := setup.endpoint.Set(ctx, "ENV_CREDENTIAL", "replacement"); err == nil {
		t.Fatal("environment was writable")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	result := receive(t, setup.send(t, credentialactor.Resolve{
		Context: cancelled,
		Ref:     "STORED_CREDENTIAL",
	})).(credentialactor.Resolution)
	if !errors.Is(result.Err, context.Canceled) {
		t.Fatalf("cancel identity: %v", result.Err)
	}
}
