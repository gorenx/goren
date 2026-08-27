package projection

import (
	"encoding/json"
	"testing"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
)

func TestBoundProjectionKeepsBindingConfigMaterializationAndAppliedSeparate(
	t *testing.T,
) {
	t.Parallel()
	unit := boundUnit{}
	state, err := unit.InitialState()
	if err != nil {
		t.Fatal(err)
	}
	events := []session.Event{
		boundProjectionEvent(
			t,
			subagent.BoundBindingEventName,
			0,
			subagent.BoundBindingData{
				Version:        subagent.BoundEventVersion,
				ChildSessionID: "child",
				Creation: subagent.BoundCreation{
					SeedBuilder: "spawn",
					Title:       "researcher",
					InitialPrompt: []agentmessage.ContentBlock{
						agentmessage.NewTextBlock("start"),
					},
				},
			},
		),
		boundProjectionEvent(
			t,
			subagent.BoundConfigEventName,
			1,
			subagent.BoundConfigData{
				Version:          subagent.BoundEventVersion,
				ChildSessionID:   "child",
				PreviousRevision: 0,
				Revision:         1,
				Config: subagent.BoundConfigSnapshot{
					Enabled: true,
				},
			},
		),
		boundProjectionEvent(
			t,
			subagent.BoundMaterializationEventName,
			2,
			subagent.BoundMaterializationData{
				Version:        subagent.BoundEventVersion,
				ChildSessionID: "child",
				ConfigRevision: 1,
				Result:         subagent.BoundMaterializationSucceeded,
			},
		),
		boundProjectionEvent(
			t,
			subagent.BoundConfigAppliedEventName,
			3,
			subagent.BoundConfigAppliedData{
				Version:              subagent.BoundEventVersion,
				ParentSessionID:      "parent",
				ParentConfigEventSeq: 1,
				Revision:             1,
			},
		),
	}
	for _, committed := range events {
		transition, applyErr := unit.ApplyState(state, committed)
		if applyErr != nil {
			t.Fatal(applyErr)
		}
		state = transition.State
	}
	viewValue, err := unit.ViewState(state)
	if err != nil {
		t.Fatal(err)
	}
	view, found, err := ReadBound(map[string]json.RawMessage{
		boundKey: viewValue,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !found || len(view.Bindings) != 1 || len(view.Configs) != 1 ||
		len(view.Materializations) != 1 || len(view.Applied) != 1 {
		t.Fatalf("Bound view = %#v, found = %v", view, found)
	}
}

func TestBoundProjectionRejectsNonContiguousConfigRevision(t *testing.T) {
	t.Parallel()
	unit := boundUnit{}
	state, err := unit.InitialState()
	if err != nil {
		t.Fatal(err)
	}
	transition, err := unit.ApplyState(
		state,
		boundProjectionEvent(
			t,
			subagent.BoundBindingEventName,
			0,
			subagent.BoundBindingData{
				Version:        subagent.BoundEventVersion,
				ChildSessionID: "child",
				Creation: subagent.BoundCreation{
					SeedBuilder: "spawn",
					Title:       "researcher",
					InitialPrompt: []agentmessage.ContentBlock{
						agentmessage.NewTextBlock("start"),
					},
				},
			},
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	state = transition.State
	_, err = unit.ApplyState(
		state,
		boundProjectionEvent(
			t,
			subagent.BoundConfigEventName,
			0,
			subagent.BoundConfigData{
				Version:          subagent.BoundEventVersion,
				ChildSessionID:   "child",
				PreviousRevision: 1,
				Revision:         2,
				Config: subagent.BoundConfigSnapshot{
					Enabled: true,
				},
			},
		),
	)
	if err == nil {
		t.Fatal("Bound projection accepted config without revision 1")
	}
}

func TestBoundProjectionRejectsConfigWithoutBinding(t *testing.T) {
	t.Parallel()
	unit := boundUnit{}
	state, err := unit.InitialState()
	if err != nil {
		t.Fatal(err)
	}
	_, err = unit.ApplyState(
		state,
		boundProjectionEvent(
			t,
			subagent.BoundConfigEventName,
			0,
			subagent.BoundConfigData{
				Version:        subagent.BoundEventVersion,
				ChildSessionID: "child",
				Revision:       1,
				Config: subagent.BoundConfigSnapshot{
					Enabled: true,
				},
			},
		),
	)
	if err == nil {
		t.Fatal("Bound projection accepted config without binding")
	}
}

func TestBoundProjectionRejectsMaterializationWithoutMatchingConfig(
	t *testing.T,
) {
	t.Parallel()
	unit := boundUnit{}
	state, err := unit.InitialState()
	if err != nil {
		t.Fatal(err)
	}
	_, err = unit.ApplyState(
		state,
		boundProjectionEvent(
			t,
			subagent.BoundMaterializationEventName,
			0,
			subagent.BoundMaterializationData{
				Version:        subagent.BoundEventVersion,
				ChildSessionID: "child",
				ConfigRevision: 1,
				Result:         subagent.BoundMaterializationSucceeded,
			},
		),
	)
	if err == nil {
		t.Fatal("Bound projection accepted materialization without config")
	}
}

func boundProjectionEvent(
	t *testing.T,
	eventType string,
	sequence int64,
	payload any,
) session.Event {
	t.Helper()
	rawValue, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return session.Event{
		Type: eventType,
		Seq:  sequence,
		Data: rawValue,
	}
}
