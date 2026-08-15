package projection

import (
	"context"
	"encoding/json"
	"reflect"
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

func (projectionFixtureUnit) ApplyState(state json.RawMessage, committed session.Event) (Transition, error) {
	if committed.Type != "fixture/projection-value" {
		return Transition{State: state}, nil
	}
	var value string
	if err := json.Unmarshal(committed.Data, &value); err != nil {
		return Transition{}, err
	}
	rawValue, err := json.Marshal(value)
	return Transition{State: rawValue, Changed: true}, err
}

func (projectionFixtureUnit) ViewState(state json.RawMessage) (json.RawMessage, error) {
	return state, nil
}

type projectionFixturePlugin struct {
	registry *DriveRegistry
	store    *session.MemoryStore
	scope    *plugin.Scope
}

func (*projectionFixturePlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "fixture-session-projection",
		Provides: []plugin.ServiceRef{
			Service.Ref(), session.StoreService.Ref(),
		},
	}
}

func (instance *projectionFixturePlugin) Apply(requestContext context.Context, pluginScope *plugin.Scope) error {
	projectionRegistry, err := NewDriveRegistry(pluginScope)
	if err != nil {
		return err
	}
	store, err := session.NewMemoryStore(pluginScope, session.MemoryStoreOptions{})
	if err != nil {
		return err
	}
	if err := pluginScope.Effect(requestContext, "sessions", func(context.Context) (plugin.Disposer, error) {
		return store.Close, nil
	}); err != nil {
		return err
	}
	instance.registry = projectionRegistry
	instance.store = store
	instance.scope = pluginScope
	if _, err := plugin.Provide(pluginScope, Service, Registry(projectionRegistry)); err != nil {
		return err
	}
	_, err = plugin.Provide(pluginScope, session.StoreService, session.Store(store))
	return err
}

func TestDriveRegistryFoldsLateStateAndPublishesWholeValues(t *testing.T) {
	t.Parallel()
	requestContext := context.Background()
	engine := plugin.NewRuntime()
	provider := &projectionFixturePlugin{}
	providerHandle, err := engine.Load(requestContext, provider)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Unload(requestContext, providerHandle) })
	conversation, err := provider.store.Create(requestContext, provider.scope, nil, session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.Append(conversation, projectionFixtureEvent, "before-registration"); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.registry.Register(provider.scope, projectionFixtureUnit{key: "fixture", version: 1}); err != nil {
		t.Fatal(err)
	}

	initial, err := provider.registry.Snapshot(conversation)
	if err != nil {
		t.Fatal(err)
	}
	if initial.AsOfSeq != 0 || string(initial.Values["fixture"]) != `"before-registration"` {
		t.Fatalf("late snapshot = %#v", initial)
	}
	changes := make([]Change, 0)
	if _, err := provider.registry.OnChanged(provider.scope, ChangeListenerFunc(func(projectionChange Change) {
		changes = append(changes, projectionChange)
	})); err != nil {
		t.Fatal(err)
	}
	committed, err := session.Append(conversation, projectionFixtureEvent, "after-registration")
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].Key != "fixture" || changes[0].Seq != committed.Seq ||
		string(changes[0].Value) != `"after-registration"` {
		t.Fatalf("changes = %#v", changes)
	}
}

func TestDriveRegistryReferenceCountsCompatibleUnits(t *testing.T) {
	t.Parallel()
	requestContext := context.Background()
	engine := plugin.NewRuntime()
	provider := &projectionFixturePlugin{}
	providerHandle, err := engine.Load(requestContext, provider)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Unload(requestContext, providerHandle) })
	firstRelease, err := provider.registry.Register(provider.scope, projectionFixtureUnit{key: "fixture", version: 1})
	if err != nil {
		t.Fatal(err)
	}
	secondRelease, err := provider.registry.Register(provider.scope, projectionFixtureUnit{key: "fixture", version: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.registry.Register(provider.scope, projectionFixtureUnit{key: "fixture", version: 2}); err == nil {
		t.Fatal("stateVersion mismatch was accepted")
	}
	conversation, err := provider.store.Create(requestContext, provider.scope, nil, session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := firstRelease(requestContext); err != nil {
		t.Fatal(err)
	}
	projectionSnapshot, err := provider.registry.Snapshot(conversation)
	if err != nil || len(projectionSnapshot.Values) != 1 {
		t.Fatalf("snapshot after first release = (%#v, %v)", projectionSnapshot, err)
	}
	if err := secondRelease(requestContext); err != nil {
		t.Fatal(err)
	}
	projectionSnapshot, err = provider.registry.Snapshot(conversation)
	if err != nil || len(projectionSnapshot.Values) != 0 {
		t.Fatalf("snapshot after last release = (%#v, %v)", projectionSnapshot, err)
	}
}

func TestDriveRegistryCheckpointRestoreUsesVersionedTail(t *testing.T) {
	t.Parallel()
	requestContext := context.Background()
	engine := plugin.NewRuntime()
	provider := &projectionFixturePlugin{}
	providerHandle, err := engine.Load(requestContext, provider)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Unload(requestContext, providerHandle) })
	if _, err := provider.registry.Register(provider.scope, projectionFixtureUnit{key: "fixture", version: 1}); err != nil {
		t.Fatal(err)
	}
	conversation, err := provider.store.Create(requestContext, provider.scope, nil, session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.Append(conversation, projectionFixtureEvent, "one"); err != nil {
		t.Fatal(err)
	}
	rows, err := provider.registry.Checkpoint(conversation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.Append(conversation, projectionFixtureEvent, "two"); err != nil {
		t.Fatal(err)
	}
	floor := provider.registry.RestoreFloor(rows)
	if floor == nil || *floor != 0 {
		t.Fatalf("restore floor = %v", floor)
	}
	restored, err := provider.registry.Restore(rows, conversation.Events(), *floor)
	if err != nil {
		t.Fatal(err)
	}
	if string(restored.Snapshot.Values["fixture"]) != `"two"` || restored.Snapshot.AsOfSeq != 1 {
		t.Fatalf("restored snapshot = %#v", restored.Snapshot)
	}
	wantRow := CheckpointRow{Version: 1, Seq: 1, Value: json.RawMessage(`"two"`)}
	if !reflect.DeepEqual(restored.Checkpoint["fixture"], wantRow) {
		t.Fatalf("restored row = %#v", restored.Checkpoint["fixture"])
	}
	if _, err := provider.registry.Restore(Checkpoint{}, conversation.Events()[1:], 1); err == nil {
		t.Fatal("partial restore without a checkpoint succeeded")
	}
}
