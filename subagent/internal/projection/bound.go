package projection

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/gorenx/goren/session"
	sessionprojection "github.com/gorenx/goren/session/projection"
	boundcontract "github.com/gorenx/goren/subagent/bound"
)

const boundKey = "subagent-bound"

// BoundBinding is one immutable Definition-to-child relation in a user
// Session. ContextNextSeq freezes the fresh-child context prefix.
type BoundBinding struct {
	Name           string            `json:"name"`
	ChildSessionID session.SessionID `json:"childSessionId"`
	ContextNextSeq int64             `json:"contextNextSeq"`
	Seq            int64             `json:"seq"`
}

// BoundMaterialization is the latest create or restore result for a Binding.
type BoundMaterialization struct {
	Name               string                              `json:"name"`
	ChildSessionID     session.SessionID                   `json:"childSessionId"`
	DefinitionRevision int64                               `json:"definitionRevision"`
	Result             boundcontract.MaterializationResult `json:"result"`
	Seq                int64                               `json:"seq"`
}

// BoundApplied is one complete Definition installed in a child Session.
type BoundApplied struct {
	Definition boundcontract.Definition `json:"definition"`
	Seq        int64                    `json:"seq"`
}

// Bound contains separate derived views over parent Binding/materialization
// facts and child Applied Definition history from one Session prefix.
type Bound struct {
	Bindings         []BoundBinding         `json:"bindings"`
	Materializations []BoundMaterialization `json:"materializations"`
	Applied          []BoundApplied         `json:"applied"`
}

type boundUnit struct{}

// FoldBound rebuilds the Bound view from one committed Session prefix. It is
// used by state-dependent WritePlans at the same FIFO head as projection.
func FoldBound(events []session.Event) (Bound, error) {
	unit := boundUnit{}
	state, err := unit.InitialState()
	if err != nil {
		return Bound{}, err
	}
	for _, committed := range events {
		transition, applyErr := unit.ApplyState(state, committed)
		if applyErr != nil {
			return Bound{}, applyErr
		}
		state = transition.State
	}
	return decodeBoundState(state)
}

func (boundUnit) Key() string {
	return boundKey
}

func (boundUnit) StateVersion() int64 {
	return 2
}

func (boundUnit) InitialState() (json.RawMessage, error) {
	return json.Marshal(Bound{
		Bindings:         []BoundBinding{},
		Materializations: []BoundMaterialization{},
		Applied:          []BoundApplied{},
	})
}

func (boundUnit) ApplyState(
	state json.RawMessage,
	committed session.Event,
) (sessionprojection.Transition, error) {
	current, err := decodeBoundState(state)
	if err != nil {
		return sessionprojection.Transition{}, err
	}
	switch committed.Type {
	case boundcontract.BindingEventName:
		err = applyBoundBinding(&current, committed)
	case boundcontract.MaterializationEventName:
		err = applyBoundMaterialization(&current, committed)
	case boundcontract.DefinitionAppliedEventName:
		err = applyBoundDefinition(&current, committed)
	default:
		return sessionprojection.Transition{
			State:   append(json.RawMessage(nil), state...),
			Changed: false,
		}, nil
	}
	if err != nil {
		return sessionprojection.Transition{}, err
	}
	next, err := json.Marshal(current)
	if err != nil {
		return sessionprojection.Transition{}, err
	}
	return sessionprojection.Transition{
		State:   next,
		Changed: !bytes.Equal(state, next),
	}, nil
}

func (boundUnit) ViewState(state json.RawMessage) (json.RawMessage, error) {
	current, err := decodeBoundState(state)
	if err != nil {
		return nil, err
	}
	return json.Marshal(current)
}

func applyBoundBinding(current *Bound, committed session.Event) error {
	var data boundcontract.BindingData
	if err := decodeJSON(committed.Data, &data); err != nil {
		return fmt.Errorf("subagent: decode Bound binding: %w", err)
	}
	if data.Version != boundcontract.EventVersion ||
		!validBoundName(data.Name) ||
		!validSessionID(data.ChildSessionID) ||
		data.ContextNextSeq < 0 ||
		data.ContextNextSeq > committed.Seq {
		return errors.New("subagent: invalid Bound binding event")
	}
	for _, existing := range current.Bindings {
		if existing.Name == data.Name ||
			existing.ChildSessionID == data.ChildSessionID {
			return errors.New("subagent: duplicate Bound binding")
		}
	}
	current.Bindings = append(
		current.Bindings,
		BoundBinding{
			Name:           data.Name,
			ChildSessionID: data.ChildSessionID,
			ContextNextSeq: data.ContextNextSeq,
			Seq:            committed.Seq,
		},
	)
	sort.Slice(current.Bindings, func(leftIndex int, rightIndex int) bool {
		return current.Bindings[leftIndex].Name <
			current.Bindings[rightIndex].Name
	})
	return nil
}

func applyBoundMaterialization(current *Bound, committed session.Event) error {
	var data boundcontract.MaterializationData
	if err := decodeJSON(committed.Data, &data); err != nil {
		return fmt.Errorf("subagent: decode Bound materialization: %w", err)
	}
	if data.Version != boundcontract.EventVersion ||
		!validBoundName(data.Name) ||
		!validSessionID(data.ChildSessionID) ||
		data.DefinitionRevision <= 0 ||
		(data.Result != boundcontract.MaterializationSucceeded &&
			data.Result != boundcontract.MaterializationFailed) {
		return errors.New("subagent: invalid Bound materialization event")
	}
	bindingValue, found := current.BindingNamed(data.Name)
	if !found || bindingValue.ChildSessionID != data.ChildSessionID {
		return errors.New(
			"subagent: Bound materialization has no matching binding",
		)
	}
	next := BoundMaterialization{
		Name:               data.Name,
		ChildSessionID:     data.ChildSessionID,
		DefinitionRevision: data.DefinitionRevision,
		Result:             data.Result,
		Seq:                committed.Seq,
	}
	for index, existing := range current.Materializations {
		if existing.Name == data.Name {
			current.Materializations[index] = next
			return nil
		}
	}
	current.Materializations = append(current.Materializations, next)
	sort.Slice(
		current.Materializations,
		func(leftIndex int, rightIndex int) bool {
			return current.Materializations[leftIndex].Name <
				current.Materializations[rightIndex].Name
		},
	)
	return nil
}

func applyBoundDefinition(current *Bound, committed session.Event) error {
	var data boundcontract.DefinitionAppliedData
	if err := decodeJSON(committed.Data, &data); err != nil {
		return fmt.Errorf("subagent: decode applied Bound Definition: %w", err)
	}
	definitionValue, err := boundcontract.SnapshotDefinition(data.Definition)
	if data.Version != boundcontract.EventVersion || err != nil ||
		!definitionValue.Enabled {
		return errors.New("subagent: invalid applied Bound Definition event")
	}
	current.Applied = append(
		current.Applied,
		BoundApplied{
			Definition: definitionValue,
			Seq:        committed.Seq,
		},
	)
	return nil
}

func decodeBoundState(rawValue json.RawMessage) (Bound, error) {
	var current Bound
	if err := decodeJSON(rawValue, &current); err != nil {
		return Bound{}, err
	}
	if current.Bindings == nil || current.Materializations == nil ||
		current.Applied == nil {
		return Bound{}, errors.New("subagent: invalid Bound projection state")
	}
	return current, nil
}

// ReadBound returns the complete trustworthy Bound view from one projection
// snapshot. Missing means the Bound unit was not registered.
func ReadBound(values sessionprojection.Values) (Bound, bool, error) {
	rawValue, found := values[boundKey]
	if !found {
		return Bound{}, false, nil
	}
	current, err := decodeBoundState(rawValue)
	return current, true, err
}

func (current Bound) Binding(
	childID session.SessionID,
) (BoundBinding, bool) {
	for _, bindingValue := range current.Bindings {
		if bindingValue.ChildSessionID == childID {
			return bindingValue, true
		}
	}
	return BoundBinding{}, false
}

func (current Bound) BindingNamed(name string) (BoundBinding, bool) {
	for _, bindingValue := range current.Bindings {
		if bindingValue.Name == name {
			return bindingValue, true
		}
	}
	return BoundBinding{}, false
}

func (current Bound) Materialization(
	childID session.SessionID,
) (BoundMaterialization, bool) {
	for _, materializationValue := range current.Materializations {
		if materializationValue.ChildSessionID == childID {
			return materializationValue, true
		}
	}
	return BoundMaterialization{}, false
}

func (current Bound) LatestApplied() (BoundApplied, bool) {
	if len(current.Applied) == 0 {
		return BoundApplied{}, false
	}
	latest := current.Applied[len(current.Applied)-1]
	detached, err := boundcontract.SnapshotDefinition(latest.Definition)
	if err != nil {
		return BoundApplied{}, false
	}
	latest.Definition = detached
	return latest, true
}

func validBoundName(name string) bool {
	return strings.TrimSpace(name) != "" && name == strings.TrimSpace(name)
}

func validSessionID(identifier session.SessionID) bool {
	return strings.TrimSpace(string(identifier)) != "" &&
		string(identifier) == strings.TrimSpace(string(identifier))
}

var _ sessionprojection.Unit = boundUnit{}
