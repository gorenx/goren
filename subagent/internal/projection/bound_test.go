package projection

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/gorenx/goren/session"
	sessionprojection "github.com/gorenx/goren/session/projection"
	boundcontract "github.com/gorenx/goren/subagent/bound"
)

func TestBoundProjectionKeepsBindingMaterializationAndAppliedSeparate(
	t *testing.T,
) {
	t.Parallel()
	unit := boundUnit{}
	state, err := unit.InitialState()
	if err != nil {
		t.Fatal(err)
	}
	definitionValue := boundDefinitionFixture(t, 1)
	events := []session.Event{
		boundProjectionEvent(
			t,
			boundcontract.BindingEventName,
			10,
			boundcontract.BindingData{
				Version:        boundcontract.EventVersion,
				Name:           definitionValue.Name,
				ChildSessionID: "child",
				ContextNextSeq: 8,
			},
		),
		boundProjectionEvent(
			t,
			boundcontract.MaterializationEventName,
			11,
			boundcontract.MaterializationData{
				Version:            boundcontract.EventVersion,
				Name:               definitionValue.Name,
				ChildSessionID:     "child",
				DefinitionRevision: definitionValue.Revision,
				Result:             boundcontract.MaterializationSucceeded,
			},
		),
		boundProjectionEvent(
			t,
			boundcontract.DefinitionAppliedEventName,
			0,
			boundcontract.DefinitionAppliedData{
				Version:    boundcontract.EventVersion,
				Definition: definitionValue,
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
	// Key is a projection Unit key. Value is that Unit's encoded state.
	values := sessionprojection.Values{
		boundKey: viewValue,
	}
	view, found, err := ReadBound(values)
	if err != nil {
		t.Fatal(err)
	}
	if !found || len(view.Bindings) != 1 ||
		len(view.Materializations) != 1 || len(view.Applied) != 1 {
		t.Fatalf("Bound view = %#v, found = %v", view, found)
	}
	if view.Bindings[0].ContextNextSeq != 8 ||
		view.Applied[0].Definition.SystemPrompt != definitionValue.SystemPrompt {
		t.Fatalf("Bound projection lost durable data: %#v", view)
	}
}

func TestBoundProjectionRejectsRemovedEventVersion(t *testing.T) {
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
			boundcontract.BindingEventName,
			1,
			boundcontract.BindingData{
				Version:        1,
				Name:           "researcher",
				ChildSessionID: "child",
				ContextNextSeq: 0,
			},
		),
	)
	if err == nil {
		t.Fatal("Bound projection accepted removed event version")
	}
}

func TestBoundProjectionRejectsDuplicateDefinitionName(t *testing.T) {
	t.Parallel()
	unit := boundUnit{}
	state, err := unit.InitialState()
	if err != nil {
		t.Fatal(err)
	}
	first, err := unit.ApplyState(
		state,
		boundProjectionEvent(
			t,
			boundcontract.BindingEventName,
			1,
			boundcontract.BindingData{
				Version:        boundcontract.EventVersion,
				Name:           "researcher",
				ChildSessionID: "child-a",
				ContextNextSeq: 0,
			},
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = unit.ApplyState(
		first.State,
		boundProjectionEvent(
			t,
			boundcontract.BindingEventName,
			2,
			boundcontract.BindingData{
				Version:        boundcontract.EventVersion,
				Name:           "researcher",
				ChildSessionID: "child-b",
				ContextNextSeq: 0,
			},
		),
	)
	if err == nil {
		t.Fatal("Bound projection accepted duplicate Definition name")
	}
}

func TestBoundProjectionRejectsMaterializationWithoutBinding(t *testing.T) {
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
			boundcontract.MaterializationEventName,
			0,
			boundcontract.MaterializationData{
				Version:            boundcontract.EventVersion,
				Name:               "researcher",
				ChildSessionID:     "child",
				DefinitionRevision: 1,
				Result:             boundcontract.MaterializationSucceeded,
			},
		),
	)
	if err == nil {
		t.Fatal("Bound projection accepted materialization without binding")
	}
}

func TestBoundProjectionDoesNotExposeInteractionCursor(t *testing.T) {
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
			boundcontract.CursorEventName,
			10,
			boundcontract.Cursor{
				Version:         boundcontract.EventVersion,
				Name:            "researcher",
				ChildSessionID:  "child",
				PreviousNextSeq: 1,
				NextSeq:         10,
				ThroughTurn:     2,
				Disposition:     boundcontract.CursorDelivered,
			},
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	if transition.Changed || !bytes.Equal(transition.State, state) {
		t.Fatal("Bound interaction cursor entered the public projection")
	}
}

func boundDefinitionFixture(
	testingContext *testing.T,
	revision int64,
) boundcontract.Definition {
	testingContext.Helper()
	definitionValue, err := boundcontract.NewDefinition(
		boundcontract.Draft{
			Name:         "researcher",
			Enabled:      true,
			SystemPrompt: "research",
		},
		revision,
	)
	if err != nil {
		testingContext.Fatal(err)
	}
	return definitionValue
}

func boundProjectionEvent(
	testingContext *testing.T,
	eventType string,
	sequence int64,
	payload any,
) session.Event {
	testingContext.Helper()
	rawValue, err := json.Marshal(payload)
	if err != nil {
		testingContext.Fatal(err)
	}
	return session.Event{
		Type: eventType,
		Seq:  sequence,
		Data: rawValue,
	}
}
