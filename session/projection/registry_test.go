package projection

import (
	"encoding/json"
	"errors"
	"testing"

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
	fail := value == subject.applyFailureValue && subject.applyFailures > 0
	if fail {
		subject.applyFailures--
	}
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
	fail := value == subject.viewFailureValue && subject.viewFailures > 0
	if fail {
		subject.viewFailures--
	}
	if fail {
		return nil, errors.New("injected view failure")
	}
	return append(json.RawMessage(nil), state...), nil
}

func historyOf(values ...string) []session.Event {
	var events []session.Event
	for index, value := range values {
		encoded, _ := json.Marshal(value)
		events = append(events, session.Event{
			Seq:  int64(index),
			Type: projectionFixtureEventName,
			Data: encoded,
		})
	}
	return events
}

func registerUnit(t *testing.T, owner *Projection, definition Unit) RegistrationID {
	t.Helper()
	id, err := owner.registry.Register(definition)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestRegistryLateFoldAndDuplicateObservation(t *testing.T) {
	owner := NewRegistry().NewProjection()
	source := historyOf("before", "after")
	registerUnit(t, owner, projectionFixtureUnit{
		key:     "fixture",
		version: 1,
	})
	prefix := source
	prefix = prefix[:1]
	initial, err := owner.Snapshot(prefix)
	if err != nil || initial.AsOfSeq != 0 || string(initial.Values["fixture"]) != `"before"` {
		t.Fatalf("late snapshot = %#v, %v", initial, err)
	}
	changes, err := owner.Observe(source, 1)
	if err != nil || len(changes) != 1 || changes[0].Seq != 1 || string(changes[0].Value) != `"after"` {
		t.Fatalf("changes = %#v, %v", changes, err)
	}
	changes[0].Value[0] = '!'
	historical, err := owner.Snapshot(prefix)
	if err != nil || historical.AsOfSeq != 0 || string(historical.Values["fixture"]) != `"before"` {
		t.Fatalf("historical snapshot = %#v, %v", historical, err)
	}
	duplicate, err := owner.Observe(source, 1)
	if err != nil || len(duplicate) != 0 {
		t.Fatalf("duplicate = %#v, %v", duplicate, err)
	}
	current, err := owner.Snapshot(source)
	if err != nil || string(current.Values["fixture"]) != `"after"` {
		t.Fatalf("current = %#v, %v", current, err)
	}
}

func TestRegistryStagesAllUnitsBeforeInstallingState(t *testing.T) {
	for _, mode := range []string{"apply", "view"} {
		t.Run(mode, func(t *testing.T) {
			owner := NewRegistry().NewProjection()
			first := &stagedFailureUnit{
				key:            "first",
				unchangedValue: "tail",
			}
			second := &stagedFailureUnit{
				key:            "second",
				unchangedValue: "tail",
			}
			if mode == "apply" {
				second.applyFailureValue = "retry"
				second.applyFailures = 1
			} else {
				second.viewFailureValue = "retry"
				second.viewFailures = 1
			}
			registerUnit(t, owner, first)
			registerUnit(t, owner, second)
			source := historyOf("base", "retry", "tail")
			changes, err := owner.Observe(source, 0)
			if err != nil || len(changes) != 2 {
				t.Fatalf("base = %#v, %v", changes, err)
			}
			changes, err = owner.Observe(source, 1)
			if err == nil || len(changes) != 0 {
				t.Fatalf("failure = %#v, %v", changes, err)
			}
			for key, entry := range owner.registry.registrations {
				cell := owner.cells[entry]
				if cell.observedSeq != 0 || string(cell.state) != `"base"` {
					t.Fatalf("partial install %s: %#v", key, cell)
				}
			}
			if mode == "view" {
				prefix := source
				prefix = prefix[:2]
				restored, err := owner.Snapshot(prefix)
				if err != nil || restored.AsOfSeq != 1 || string(restored.Values["first"]) != `"retry"` || string(restored.Values["second"]) != `"retry"` {
					t.Fatalf("snapshot retry = %#v, %v", restored, err)
				}
			} else {
				changes, err = owner.Observe(source, 2)
				if err != nil || len(changes) != 2 {
					t.Fatalf("catchup = %#v, %v", changes, err)
				}
				for _, changed := range changes {
					if changed.Seq != 2 || string(changed.Value) != `"tail"` {
						t.Fatalf("catchup change = %#v", changed)
					}
				}
			}
		})
	}
}

func TestRegistryRegistrationIDsAndCreationIsolation(t *testing.T) {
	owner := NewRegistry().NewProjection()
	definition := projectionFixtureUnit{
		key:     "fixture",
		version: 1,
	}
	first := registerUnit(t, owner, definition)
	second := registerUnit(t, owner, definition)
	if first == second {
		t.Fatal("registrations reused identity")
	}
	incompatible := definition
	incompatible.version = 2
	if _, err := owner.registry.Register(incompatible); err == nil {
		t.Fatal("incompatible version accepted")
	}
	owner.registry.Remove(first)
	owner.registry.Remove(first)
	if len(owner.registry.registrations) != 1 {
		t.Fatal("duplicate removal released another registration")
	}
	old := historyOf("old")
	replacement := historyOf("new")
	independent := owner.registry.NewProjection()
	if _, err := owner.Snapshot(old); err != nil {
		t.Fatal(err)
	}
	if _, err := independent.Snapshot(replacement); err != nil {
		t.Fatal(err)
	}
	entry := owner.registry.registrations["fixture"]
	if string(owner.cells[entry].state) != `"old"` || string(independent.cells[entry].state) != `"new"` {
		t.Fatal("independent folds shared state")
	}
	owner.registry.Remove(second)
	if len(owner.registry.registrations) != 0 {
		t.Fatal("last removal left registration")
	}
	registerUnit(t, owner, incompatible)
	owner.registry.Remove(second)
	if owner.registry.registrations["fixture"].projectionUnit.StateVersion() != 2 {
		t.Fatal("old ID removed replacement")
	}
}

func TestRegistryCheckpointRestoreUsesVersionedTail(t *testing.T) {
	owner := NewRegistry().NewProjection()
	registerUnit(t, owner, projectionFixtureUnit{
		key:     "fixture",
		version: 1,
	})
	source := historyOf("one", "two")
	prefix := source
	prefix = prefix[:1]
	rows, err := owner.Checkpoint(prefix)
	if err != nil {
		t.Fatal(err)
	}
	floor := owner.registry.RestoreFloor(rows)
	if floor == nil || *floor != 0 {
		t.Fatalf("floor = %v", floor)
	}
	restored, err := owner.registry.Restore(rows, source, *floor)
	if err != nil || restored.Snapshot.AsOfSeq != 1 || string(restored.Snapshot.Values["fixture"]) != `"two"` || restored.Checkpoint["fixture"].Seq != 1 || restored.Checkpoint["fixture"].Version != 1 {
		t.Fatalf("restore = %#v, %v", restored, err)
	}
	if _, err := owner.registry.Restore(Checkpoint{}, source[1:], 1); err == nil {
		t.Fatal("partial restore without checkpoint accepted")
	}
	rows["fixture"].Value[0] = '!'
	current, err := owner.Checkpoint(prefix)
	if err != nil || string(current["fixture"].Value) != `"one"` {
		t.Fatalf("checkpoint escaped: %#v, %v", current, err)
	}
}

func TestProjectionRejectsIncompleteHistory(t *testing.T) {
	owner := NewRegistry().NewProjection()
	for _, source := range [][]session.Event{
		{{Seq: 1}},
		{{Seq: 0}, {Seq: 2}},
	} {
		if _, err := owner.Snapshot(source); err == nil {
			t.Fatalf("invalid history accepted: %#v", source)
		}
	}
	if _, err := owner.Observe(historyOf("one"), 1); err == nil {
		t.Fatal("missing sequence accepted")
	}
}

func TestProjectionRebuildsAfterSameKeyRegistrationReplacement(t *testing.T) {
	directory := NewRegistry()
	owner := directory.NewProjection()
	original := registerUnit(t, owner, projectionFixtureUnit{
		key:     "fixture",
		version: 1,
	})
	if _, err := owner.Snapshot(historyOf("old")); err != nil {
		t.Fatal(err)
	}
	previous := directory.registrations["fixture"]
	directory.Remove(original)
	registerUnit(t, owner, projectionFixtureUnit{
		key:     "fixture",
		version: 1,
	})
	result, err := owner.Snapshot(historyOf("new"))
	if err != nil || string(result.Values["fixture"]) != `"new"` {
		t.Fatalf("replacement reused old state: %#v, %v", result, err)
	}
	if _, found := owner.cells[previous]; found {
		t.Fatal("removed registration retained a cell")
	}
}
