package projection

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
)

func TestIdentityProjectionUsesLatestTrustworthyDescriptor(t *testing.T) {
	t.Parallel()
	oneShotLabel := "research"
	oneShotData := descriptorData(t, subagent.OneShotDescriptor{
		Provider: "spawn",
		Label:    &oneShotLabel,
	})
	continuableData := descriptorData(t, subagent.ContinuableDescriptor{
		Provider: "fork",
		Label:    "review",
	})
	boundData := descriptorData(t, subagent.BoundDescriptor{
		Provider: "spawn",
		Label:    "resident",
	})
	unit := identityUnit{}
	state, err := unit.InitialState()
	if err != nil {
		t.Fatal(err)
	}
	for _, committed := range []session.Event{
		projectionEvent(subagent.DescriptorEventName, 2, 100, oneShotData),
		projectionEvent(subagent.DescriptorEventName, 8, 200, continuableData),
		projectionEvent(subagent.DescriptorEventName, 9, 250, boundData),
	} {
		transition, applyErr := unit.ApplyState(state, committed)
		if applyErr != nil {
			t.Fatal(applyErr)
		}
		state = transition.State
	}
	view, err := unit.ViewState(state)
	if err != nil {
		t.Fatal(err)
	}
	decodedIdentity, found, err := ReadIdentity(map[string]json.RawMessage{
		identityKey: view,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !found || decodedIdentity.Mode != subagent.ModeBound ||
		decodedIdentity.Label == nil || *decodedIdentity.Label != "resident" || decodedIdentity.Seq != 9 {
		t.Fatalf("identity = %#v, found = %v", decodedIdentity, found)
	}

	transition, err := unit.ApplyState(
		state,
		projectionEvent(
			subagent.DescriptorEventName,
			10,
			300,
			json.RawMessage(`{"version":2,"mode":"continuable"}`),
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	view, err = unit.ViewState(transition.State)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err = ReadIdentity(map[string]json.RawMessage{
		identityKey: view,
	}); err != nil || found {
		t.Fatalf("damaged identity = (found %v, error %v), want null", found, err)
	}
}

func TestTimingProjectionResetsInheritedTurnsAndTracksOwnActivity(t *testing.T) {
	t.Parallel()
	unit := timingUnit{}
	state, err := unit.InitialState()
	if err != nil {
		t.Fatal(err)
	}
	events := []session.Event{
		projectionEvent(session.TurnStartEventName, 0, 100, json.RawMessage(`{}`)),
		projectionEvent(subagent.DescriptorEventName, 1, 110, json.RawMessage(`{}`)),
		projectionEvent(session.TurnEndEventName, 2, 300, json.RawMessage(`{}`)),
		projectionEvent(session.TurnStartEventName, 3, 1_000, json.RawMessage(`{}`)),
		projectionEvent(subagent.DescriptorEventName, 4, 1_100, json.RawMessage(`{}`)),
		projectionEvent(session.TurnEndEventName, 5, 4_100, json.RawMessage(`{}`)),
		projectionEvent(session.TurnStartEventName, 6, 10_000, json.RawMessage(`{}`)),
		projectionEvent("assistant/chunk", 7, 10_500, json.RawMessage(`{}`)),
	}
	for _, committed := range events {
		transition, applyErr := unit.ApplyState(state, committed)
		if applyErr != nil {
			t.Fatal(applyErr)
		}
		state = transition.State
	}
	view, err := unit.ViewState(state)
	if err != nil {
		t.Fatal(err)
	}
	var decodedTiming timing
	if err = json.Unmarshal(view, &decodedTiming); err != nil {
		t.Fatal(err)
	}
	want := timing{
		SettledMS: 3_100,
		Active: &interval{
			Since:   10_000,
			Through: 10_500,
		},
	}
	if !reflect.DeepEqual(decodedTiming, want) {
		t.Fatalf("timing = %#v, want %#v", decodedTiming, want)
	}
}

func TestProjectionViewsRejectDamagedCheckpointState(t *testing.T) {
	t.Parallel()
	identityProjection := identityUnit{}
	for _, damaged := range []json.RawMessage{
		json.RawMessage(`{"identity":{"mode":"continuable","label":null,"seq":1}}`),
		json.RawMessage(`{"identity":{"mode":"one-shot","seq":1,"extra":true}}`),
		json.RawMessage(`{"identity":null}`),
	} {
		if _, err := identityProjection.ViewState(damaged); err == nil {
			t.Fatalf("identity state %s was accepted", damaged)
		}
	}
	timingProjection := timingUnit{}
	for _, damaged := range []json.RawMessage{
		json.RawMessage(`{"settledMs":-1,"descriptorSeen":true}`),
		json.RawMessage(`{"settledMs":0,"descriptorSeen":false,"extra":true}`),
		json.RawMessage(`{"settledMs":0,"descriptorSeen":true,"active":{"since":-1,"through":0}}`),
	} {
		if _, err := timingProjection.ViewState(damaged); err == nil {
			t.Fatalf("timing state %s was accepted", damaged)
		}
	}
}

func descriptorData(t *testing.T, descriptor subagent.Descriptor) json.RawMessage {
	t.Helper()
	encodedDescriptor, err := subagent.SnapshotDescriptor(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	rawValue, err := json.Marshal(encodedDescriptor)
	if err != nil {
		t.Fatal(err)
	}
	return rawValue
}

func projectionEvent(
	eventType string,
	sequence int64,
	timestamp int64,
	data json.RawMessage,
) session.Event {
	return session.Event{
		Type: eventType,
		Seq:  sequence,
		Time: timestamp,
		Data: append(json.RawMessage(nil), data...),
	}
}
