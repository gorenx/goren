package projection

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
)

const projectionFixtureEventName = "fixture/projection-value"

var projectionFixtureEvent = session.DefineEvent[string](projectionFixtureEventName)

type projectionFixtureUnit struct {
	key     string
	version int64
}

type stagedFailureUnit struct {
	key               string
	mu                sync.Mutex
	applyFailureValue string
	applyFailures     int
	viewFailureValue  string
	viewFailures      int
	unchangedValue    string
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
	if committed.Type != projectionFixtureEventName {
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

func (subject *stagedFailureUnit) Key() string {
	return subject.key
}

func (*stagedFailureUnit) StateVersion() int64 {
	return 1
}

func (*stagedFailureUnit) InitialState() (json.RawMessage, error) {
	return json.RawMessage(`null`), nil
}

func (subject *stagedFailureUnit) ApplyState(
	state json.RawMessage,
	committed session.Event,
) (Transition, error) {
	if committed.Type != projectionFixtureEventName {
		return Transition{
			State: state,
		}, nil
	}
	var value string
	if err := json.Unmarshal(committed.Data, &value); err != nil {
		return Transition{}, err
	}
	subject.mu.Lock()
	fail := value == subject.applyFailureValue && subject.applyFailures > 0
	if fail {
		subject.applyFailures--
	}
	subject.mu.Unlock()
	if fail {
		return Transition{}, errors.New("injected apply failure")
	}
	rawValue, err := json.Marshal(value)
	return Transition{
		State:   rawValue,
		Changed: value != subject.unchangedValue,
	}, err
}

func (subject *stagedFailureUnit) ViewState(
	state json.RawMessage,
) (json.RawMessage, error) {
	var value string
	if err := json.Unmarshal(state, &value); err != nil {
		return nil, err
	}
	subject.mu.Lock()
	fail := value == subject.viewFailureValue && subject.viewFailures > 0
	if fail {
		subject.viewFailures--
	}
	subject.mu.Unlock()
	if fail {
		return nil, errors.New("injected view failure")
	}
	return append(json.RawMessage(nil), state...), nil
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
			plugin.EventOf[Changed](),
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
	projectionChange, matches := fact.(Changed)
	if !matches {
		return nil
	}
	observer.mu.Lock()
	observer.changes = append(observer.changes, projectionChange.Change)
	observer.mu.Unlock()
	return nil
}

func (observer *projectionFixtureObserver) observedChanges() []Change {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	return append([]Change(nil), observer.changes...)
}

type projectionFixture struct {
	store    session.LiveStore
	registry *DriveRegistry
	observer *projectionFixtureObserver
}

type sessionStoreProbe struct {
	plugin.Base
	store session.LiveStore
}

func (*sessionStoreProbe) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "session-projection-store-probe",
		Requires: []plugin.ServiceType{
			plugin.ServiceOf[session.LiveStore](),
		},
	}
}

func (probe *sessionStoreProbe) Apply(context.Context) error {
	liveStore, err := plugin.Require[session.LiveStore](probe)
	if err != nil {
		return err
	}
	probe.store = liveStore
	return nil
}

func (*sessionStoreProbe) Dispose(context.Context) error { return nil }

func newProjectionFixture(testingContext *testing.T) projectionFixture {
	testingContext.Helper()
	reporter := projectionFailureReporter{}
	sessionPlugin, err := session.NewPlugin(
		session.MemoryStoreOptions{
			PostCommitFailures: reporter,
		},
	)
	if err != nil {
		testingContext.Fatal(err)
	}
	projectionService := NewDriveRegistry()
	observer := &projectionFixtureObserver{}
	storeProbe := &sessionStoreProbe{}
	runtimeEngine := plugin.NewRuntime(
		plugin.RuntimeSettings{
			EventFailures: reporter,
		},
	)
	if _, err := runtimeEngine.Start(
		context.Background(),
		observer,
		projectionService,
		sessionPlugin,
		storeProbe,
	); err != nil {
		testingContext.Fatal(err)
	}
	testingContext.Cleanup(func() {
		if err := runtimeEngine.Shutdown(context.Background()); err != nil {
			testingContext.Error(err)
		}
	})
	return projectionFixture{
		store:    storeProbe.store,
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

func TestDriveRegistryStagesCellsAndCatchesUpAfterApplyFailure(t *testing.T) {
	t.Parallel()
	fixture := newProjectionFixture(t)
	first := &stagedFailureUnit{
		key:            "first",
		unchangedValue: "tail",
	}
	second := &stagedFailureUnit{
		key:               "second",
		applyFailureValue: "retry",
		applyFailures:     1,
		unchangedValue:    "tail",
	}
	for _, projectionUnit := range []Unit{first, second} {
		if _, err := fixture.registry.Register(projectionUnit); err != nil {
			t.Fatal(err)
		}
	}
	handle, err := fixture.store.Create(context.Background(), nil, session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	conversation := handle.Session()
	base := commitProjectionFixtureValue(t, conversation, "base")
	assertProjectionFixtureCells(t, fixture.registry, conversation, base.Seq, `"base"`)
	if changes := fixture.observer.observedChanges(); len(changes) != 2 {
		t.Fatalf("base changes = %#v", changes)
	}

	failed := commitProjectionFixtureValue(t, conversation, "retry")
	assertProjectionFixtureCells(t, fixture.registry, conversation, base.Seq, `"base"`)
	if changes := fixture.observer.observedChanges(); len(changes) != 2 {
		t.Fatalf("changes after failed seq %d = %#v", failed.Seq, changes)
	}

	recovered := commitProjectionFixtureValue(t, conversation, "tail")
	assertProjectionFixtureCells(t, fixture.registry, conversation, recovered.Seq, `"tail"`)
	changes := fixture.observer.observedChanges()
	if len(changes) != 4 {
		t.Fatalf("recovered changes = %#v", changes)
	}
	for index, projectionKey := range []string{"first", "second"} {
		observed := changes[index+2]
		if observed.Key != projectionKey || observed.Seq != recovered.Seq ||
			string(observed.Value) != `"tail"` {
			t.Fatalf("recovered change %d = %#v", index, observed)
		}
	}
}

func TestDriveRegistryStagesCellsBeforeValidatingAllViews(t *testing.T) {
	t.Parallel()
	fixture := newProjectionFixture(t)
	first := &stagedFailureUnit{
		key: "first",
	}
	second := &stagedFailureUnit{
		key:              "second",
		viewFailureValue: "retry-view",
		viewFailures:     1,
	}
	for _, projectionUnit := range []Unit{first, second} {
		if _, err := fixture.registry.Register(projectionUnit); err != nil {
			t.Fatal(err)
		}
	}
	handle, err := fixture.store.Create(context.Background(), nil, session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	conversation := handle.Session()
	base := commitProjectionFixtureValue(t, conversation, "base")
	assertProjectionFixtureCells(t, fixture.registry, conversation, base.Seq, `"base"`)

	failed := commitProjectionFixtureValue(t, conversation, "retry-view")
	assertProjectionFixtureCells(t, fixture.registry, conversation, base.Seq, `"base"`)
	if changes := fixture.observer.observedChanges(); len(changes) != 2 {
		t.Fatalf("changes after failed view at seq %d = %#v", failed.Seq, changes)
	}

	projectionSnapshot, err := fixture.registry.Snapshot(conversation)
	if err != nil {
		t.Fatal(err)
	}
	if projectionSnapshot.AsOfSeq != failed.Seq ||
		string(projectionSnapshot.Values["first"]) != `"retry-view"` ||
		string(projectionSnapshot.Values["second"]) != `"retry-view"` {
		t.Fatalf("recovered snapshot = %#v", projectionSnapshot)
	}
	assertProjectionFixtureCells(t, fixture.registry, conversation, failed.Seq, `"retry-view"`)
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

func commitProjectionFixtureValue(
	testingContext *testing.T,
	conversation session.Context,
	value string,
) session.Event {
	testingContext.Helper()
	draft, err := session.NewEventDraft(projectionFixtureEvent, value)
	if err != nil {
		testingContext.Fatal(err)
	}
	receipt, err := conversation.Commit(context.Background(), session.Batch(draft))
	if err != nil {
		testingContext.Fatal(err)
	}
	if len(receipt.Events) != 1 {
		testingContext.Fatalf("committed events = %#v", receipt.Events)
	}
	return receipt.Events[0]
}

func assertProjectionFixtureCells(
	testingContext *testing.T,
	registryOwner *DriveRegistry,
	conversation session.Context,
	wantSeq int64,
	wantState string,
) {
	testingContext.Helper()
	registryOwner.mu.Lock()
	defer registryOwner.mu.Unlock()
	for _, projectionKey := range []string{"first", "second"} {
		entry := registryOwner.registrations[projectionKey]
		if entry == nil {
			testingContext.Fatalf("projection %q is not registered", projectionKey)
		}
		cell, found := entry.cells[conversation]
		if !found {
			testingContext.Fatalf("projection %q cell is absent", projectionKey)
		}
		if cell.observedSeq != wantSeq || string(cell.state) != wantState {
			testingContext.Fatalf(
				"projection %q cell = {seq:%d state:%s}, want {seq:%d state:%s}",
				projectionKey,
				cell.observedSeq,
				cell.state,
				wantSeq,
				wantState,
			)
		}
	}
}
