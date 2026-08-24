package projection

import (
	"context"
	"encoding/json"
	"reflect"
	"sync"
	"testing"

	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
)

var projectionFixtureEvent = session.DefineEvent[string]("fixture/projection-value")

type projectionFixtureUnit struct {
	key     string
	version int64
}

func (definition projectionFixtureUnit) Key() string { return definition.key }

func (definition projectionFixtureUnit) StateVersion() int64 { return definition.version }

func (projectionFixtureUnit) InitialState() (json.RawMessage, error) {
	return json.RawMessage(`null`), nil
}

func (projectionFixtureUnit) ApplyState(
	state json.RawMessage,
	committed session.Event,
) (Transition, error) {
	if committed.Type != "fixture/projection-value" {
		return Transition{
			State: state,
		}, nil
	}
	var value string
	if err := json.Unmarshal(committed.Data, &value); err != nil {
		return Transition{}, err
	}
	rawValue, err := json.Marshal(value)
	return Transition{
		State:   rawValue,
		Changed: true,
	}, err
}

func (projectionFixtureUnit) ViewState(state json.RawMessage) (json.RawMessage, error) {
	return state, nil
}

type projectionFailureReporter struct{}

func (projectionFailureReporter) ReportEventFailure(context.Context, plugin.EventFailure) {}

func (projectionFailureReporter) ReportPostCommitFailure(session.PostCommitFailure) {}

type projectionFixtureObserver struct {
	plugin.Base
	mu       sync.Mutex
	registry Registry
	changes  []Change
}

func (*projectionFixtureObserver) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "fixture-session-projection-observer",
		Requires: []plugin.ServiceType{
			plugin.ServiceOf[Registry](),
		},
		Events: []plugin.EventSubscription{
			plugin.EventOf[ProjectionChanged](),
		},
	}
}

func (observer *projectionFixtureObserver) Apply(context.Context) error {
	projectionService, err := plugin.Require[Registry](observer)
	if err != nil {
		return err
	}
	observer.registry = projectionService
	return nil
}

func (*projectionFixtureObserver) Dispose(context.Context) error {
	return nil
}

func (observer *projectionFixtureObserver) ObserveEvent(
	_ context.Context,
	fact plugin.Event,
) error {
	projectionChange, matches := fact.(ProjectionChanged)
	if !matches {
		return nil
	}
	observer.mu.Lock()
	observer.changes = append(observer.changes, cloneChange(projectionChange.Change))
	observer.mu.Unlock()
	return nil
}

func (observer *projectionFixtureObserver) observedChanges() []Change {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	return append([]Change(nil), observer.changes...)
}

type projectionFixture struct {
	store    *session.MemoryStore
	registry *DriveRegistry
	observer *projectionFixtureObserver
}

func newProjectionFixture(testingContext *testing.T) projectionFixture {
	testingContext.Helper()
	reporter := projectionFailureReporter{}
	store, err := session.NewMemoryStore(
		session.MemoryStoreOptions{
			PostCommitFailures: reporter,
		},
	)
	if err != nil {
		testingContext.Fatal(err)
	}
	projectionService := NewDriveRegistry()
	observer := &projectionFixtureObserver{}
	runtimeEngine := plugin.NewRuntime(
		plugin.RuntimeSettings{
			EventFailures: reporter,
		},
	)
	if _, err := runtimeEngine.Start(
		context.Background(),
		observer,
		projectionService,
		store,
	); err != nil {
		testingContext.Fatal(err)
	}
	testingContext.Cleanup(func() {
		if err := runtimeEngine.Shutdown(context.Background()); err != nil {
			testingContext.Error(err)
		}
	})
	return projectionFixture{
		store:    store,
		registry: projectionService,
		observer: observer,
	}
}

func TestDriveRegistryFoldsLateStateAndPublishesWholeValues(t *testing.T) {
	t.Parallel()
	requestContext := context.Background()
	fixture := newProjectionFixture(t)
	handle, err := fixture.store.Create(requestContext, nil, session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	conversation := handle.Session()
	{
		draft, err := session.NewEventDraft(projectionFixtureEvent,
			"before-registration")
		if err == nil {
			_, err = conversation.Commit(context.Background(), session.Batch(draft))
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fixture.registry.Register(
		projectionFixtureUnit{
			key:     "fixture",
			version: 1,
		},
	); err != nil {
		t.Fatal(err)
	}

	initial, err := fixture.registry.Snapshot(conversation)
	if err != nil {
		t.Fatal(err)
	}
	if initial.AsOfSeq != 0 || string(initial.Values["fixture"]) != `"before-registration"` {
		t.Fatalf("late snapshot = %#v", initial)
	}
	draft, err := session.NewEventDraft(projectionFixtureEvent,
		"after-registration")
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := conversation.Commit(context.Background(), session.Batch(draft))
	if err != nil {
		t.Fatal(err)
	}
	committed := receipt.Events[0]
	changes := fixture.observer.observedChanges()
	if len(changes) != 1 || changes[0].Key != "fixture" || changes[0].Seq != committed.Seq ||
		string(changes[0].Value) != `"after-registration"` {
		t.Fatalf("changes = %#v", changes)
	}
}

func TestDriveRegistryReferenceCountsCompatibleUnits(t *testing.T) {
	t.Parallel()
	requestContext := context.Background()
	fixture := newProjectionFixture(t)
	firstHandle, err := fixture.registry.Register(
		projectionFixtureUnit{
			key:     "fixture",
			version: 1,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	secondHandle, err := fixture.registry.Register(
		projectionFixtureUnit{
			key:     "fixture",
			version: 1,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.registry.Register(
		projectionFixtureUnit{
			key:     "fixture",
			version: 2,
		},
	); err == nil {
		t.Fatal("stateVersion mismatch was accepted")
	}
	handle, err := fixture.store.Create(requestContext, nil, session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	conversation := handle.Session()
	if err := firstHandle.Release(requestContext); err != nil {
		t.Fatal(err)
	}
	projectionSnapshot, err := fixture.registry.Snapshot(conversation)
	if err != nil || len(projectionSnapshot.Values) != 1 {
		t.Fatalf("snapshot after first release = (%#v, %v)", projectionSnapshot, err)
	}
	if err := secondHandle.Release(requestContext); err != nil {
		t.Fatal(err)
	}
	projectionSnapshot, err = fixture.registry.Snapshot(conversation)
	if err != nil || len(projectionSnapshot.Values) != 0 {
		t.Fatalf("snapshot after last release = (%#v, %v)", projectionSnapshot, err)
	}
}

func TestDriveRegistryCheckpointRestoreUsesVersionedTail(t *testing.T) {
	t.Parallel()
	requestContext := context.Background()
	fixture := newProjectionFixture(t)
	if _, err := fixture.registry.Register(
		projectionFixtureUnit{
			key:     "fixture",
			version: 1,
		},
	); err != nil {
		t.Fatal(err)
	}
	handle, err := fixture.store.Create(requestContext, nil, session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	conversation := handle.Session()
	{
		draft, err := session.NewEventDraft(projectionFixtureEvent, "one")
		if err == nil {
			_, err = conversation.Commit(context.Background(), session.Batch(draft))
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	rows, err := fixture.registry.Checkpoint(conversation)
	if err != nil {
		t.Fatal(err)
	}
	{
		draft, err := session.NewEventDraft(projectionFixtureEvent, "two")
		if err == nil {
			_, err = conversation.Commit(context.Background(), session.Batch(draft))
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	floor := fixture.registry.RestoreFloor(rows)
	if floor == nil || *floor != 0 {
		t.Fatalf("restore floor = %v", floor)
	}
	restored, err := fixture.registry.Restore(rows, conversation.Events(), *floor)
	if err != nil {
		t.Fatal(err)
	}
	if string(restored.Snapshot.Values["fixture"]) != `"two"` || restored.Snapshot.AsOfSeq != 1 {
		t.Fatalf("restored snapshot = %#v", restored.Snapshot)
	}
	wantRow := CheckpointRow{
		Version: 1,
		Seq:     1,
		Value:   json.RawMessage(`"two"`),
	}
	if !reflect.DeepEqual(restored.Checkpoint["fixture"], wantRow) {
		t.Fatalf("restored row = %#v", restored.Checkpoint["fixture"])
	}
	if _, err := fixture.registry.Restore(
		Checkpoint{},
		conversation.Events()[1:],
		1,
	); err == nil {
		t.Fatal("partial restore without a checkpoint succeeded")
	}
}
