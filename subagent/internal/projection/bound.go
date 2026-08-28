package projection

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	sessionprojection "github.com/gorenx/goren/session/projection"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/tools"
)

const boundKey = "subagent-bound"
const maxBoundSafeInteger int64 = 1<<53 - 1

// BoundBinding is one immutable parent projection record.
type BoundBinding struct {
	ChildSessionID session.SessionID      `json:"childSessionId"`
	Creation       subagent.BoundCreation `json:"creation"`
	Seq            int64                  `json:"seq"`
}

// BoundConfig is the latest complete config for one bound child.
type BoundConfig struct {
	ChildSessionID session.SessionID            `json:"childSessionId"`
	Revision       int64                        `json:"revision"`
	Config         subagent.BoundConfigSnapshot `json:"config"`
	Seq            int64                        `json:"seq"`
}

// BoundMaterialization is the latest create/restore result for one binding.
type BoundMaterialization struct {
	ChildSessionID session.SessionID                   `json:"childSessionId"`
	ConfigRevision int64                               `json:"configRevision"`
	Result         subagent.BoundMaterializationResult `json:"result"`
	Seq            int64                               `json:"seq"`
}

// BoundApplied is one applied config reference in a child Session.
type BoundApplied struct {
	ParentSessionID      session.SessionID `json:"parentSessionId"`
	ParentConfigEventSeq int64             `json:"parentConfigEventSeq"`
	Revision             int64             `json:"revision"`
	Seq                  int64             `json:"seq"`
}

// Bound contains separate derived views over binding, config,
// materialization, and applied facts from one Session prefix.
type Bound struct {
	Bindings         []BoundBinding         `json:"bindings"`
	Configs          []BoundConfig          `json:"configs"`
	Materializations []BoundMaterialization `json:"materializations"`
	Applied          []BoundApplied         `json:"applied"`
}

type boundUnit struct{}

// FoldBound rebuilds the Bound view from one committed Session prefix. It is
// used when a state-dependent Session WritePlan must validate the FIFO-head
// snapshot with the same rules as the registered projection unit.
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
	return 1
}

func (boundUnit) InitialState() (json.RawMessage, error) {
	return json.Marshal(Bound{
		Bindings:         []BoundBinding{},
		Configs:          []BoundConfig{},
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
	case subagent.BoundBindingEventName:
		err = applyBoundBinding(&current, committed)
	case subagent.BoundConfigEventName:
		err = applyBoundConfig(&current, committed)
	case subagent.BoundMaterializationEventName:
		err = applyBoundMaterialization(&current, committed)
	case subagent.BoundConfigAppliedEventName:
		err = applyBoundApplied(&current, committed)
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
	var data subagent.BoundBindingData
	if err := decodeJSON(committed.Data, &data); err != nil {
		return fmt.Errorf("subagent: decode Bound binding: %w", err)
	}
	if data.Version != subagent.BoundEventVersion ||
		!validSessionID(data.ChildSessionID) ||
		strings.TrimSpace(data.Creation.SeedBuilder) == "" ||
		data.Creation.SeedBuilder != strings.TrimSpace(data.Creation.SeedBuilder) ||
		strings.TrimSpace(data.Creation.Title) == "" ||
		data.Creation.Title != strings.TrimSpace(data.Creation.Title) ||
		len(data.Creation.InitialPrompt) == 0 {
		return errors.New("subagent: invalid Bound binding event")
	}
	for _, existing := range current.Bindings {
		if existing.ChildSessionID == data.ChildSessionID ||
			existing.Creation.Title == data.Creation.Title {
			return errors.New("subagent: duplicate Bound binding")
		}
	}
	current.Bindings = append(
		current.Bindings,
		BoundBinding{
			ChildSessionID: data.ChildSessionID,
			Creation:       cloneBoundCreation(data.Creation),
			Seq:            committed.Seq,
		},
	)
	sort.Slice(current.Bindings, func(left int, right int) bool {
		return current.Bindings[left].ChildSessionID <
			current.Bindings[right].ChildSessionID
	})
	return nil
}

func applyBoundConfig(current *Bound, committed session.Event) error {
	var data subagent.BoundConfigData
	if err := decodeJSON(committed.Data, &data); err != nil {
		return fmt.Errorf("subagent: decode Bound config: %w", err)
	}
	if data.Version != subagent.BoundEventVersion ||
		!validSessionID(data.ChildSessionID) ||
		data.PreviousRevision < 0 ||
		data.Revision <= 0 ||
		data.Revision > maxBoundSafeInteger {
		return errors.New("subagent: invalid Bound config event")
	}
	if _, found := current.Binding(data.ChildSessionID); !found {
		return errors.New("subagent: Bound config has no binding")
	}
	index := boundConfigIndex(current.Configs, data.ChildSessionID)
	if index < 0 {
		if data.PreviousRevision != 0 || data.Revision != 1 {
			return errors.New("subagent: Bound config must start at revision 1")
		}
		current.Configs = append(
			current.Configs,
			BoundConfig{
				ChildSessionID: data.ChildSessionID,
				Revision:       data.Revision,
				Config:         cloneBoundConfig(data.Config),
				Seq:            committed.Seq,
			},
		)
		sort.Slice(current.Configs, func(left int, right int) bool {
			return current.Configs[left].ChildSessionID <
				current.Configs[right].ChildSessionID
		})
		return nil
	}
	previous := current.Configs[index]
	if data.PreviousRevision != previous.Revision ||
		data.Revision != previous.Revision+1 {
		return errors.New("subagent: Bound config revision is not contiguous")
	}
	current.Configs[index] = BoundConfig{
		ChildSessionID: data.ChildSessionID,
		Revision:       data.Revision,
		Config:         cloneBoundConfig(data.Config),
		Seq:            committed.Seq,
	}
	return nil
}

func applyBoundMaterialization(current *Bound, committed session.Event) error {
	var data subagent.BoundMaterializationData
	if err := decodeJSON(committed.Data, &data); err != nil {
		return fmt.Errorf("subagent: decode Bound materialization: %w", err)
	}
	if data.Version != subagent.BoundEventVersion ||
		!validSessionID(data.ChildSessionID) || data.ConfigRevision <= 0 ||
		(data.Result != subagent.BoundMaterializationSucceeded &&
			data.Result != subagent.BoundMaterializationFailed) {
		return errors.New("subagent: invalid Bound materialization event")
	}
	projectedConfig, found := current.Config(data.ChildSessionID)
	if !found || projectedConfig.Revision != data.ConfigRevision {
		return errors.New(
			"subagent: Bound materialization has no matching config revision",
		)
	}
	next := BoundMaterialization{
		ChildSessionID: data.ChildSessionID,
		ConfigRevision: data.ConfigRevision,
		Result:         data.Result,
		Seq:            committed.Seq,
	}
	for index, existing := range current.Materializations {
		if existing.ChildSessionID == data.ChildSessionID {
			current.Materializations[index] = next
			return nil
		}
	}
	current.Materializations = append(current.Materializations, next)
	sort.Slice(current.Materializations, func(left int, right int) bool {
		return current.Materializations[left].ChildSessionID <
			current.Materializations[right].ChildSessionID
	})
	return nil
}

func applyBoundApplied(current *Bound, committed session.Event) error {
	var data subagent.BoundConfigAppliedData
	if err := decodeJSON(committed.Data, &data); err != nil {
		return fmt.Errorf("subagent: decode Bound applied config: %w", err)
	}
	if data.Version != subagent.BoundEventVersion ||
		!validSessionID(data.ParentSessionID) ||
		data.ParentConfigEventSeq < 0 || data.Revision <= 0 {
		return errors.New("subagent: invalid Bound applied config event")
	}
	current.Applied = append(
		current.Applied,
		BoundApplied{
			ParentSessionID:      data.ParentSessionID,
			ParentConfigEventSeq: data.ParentConfigEventSeq,
			Revision:             data.Revision,
			Seq:                  committed.Seq,
		},
	)
	return nil
}

func decodeBoundState(rawValue json.RawMessage) (Bound, error) {
	var current Bound
	if err := decodeJSON(rawValue, &current); err != nil {
		return Bound{}, err
	}
	if current.Bindings == nil || current.Configs == nil ||
		current.Materializations == nil || current.Applied == nil {
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
			bindingValue.Creation = cloneBoundCreation(bindingValue.Creation)
			return bindingValue, true
		}
	}
	return BoundBinding{}, false
}

func (current Bound) Config(
	childID session.SessionID,
) (BoundConfig, bool) {
	index := boundConfigIndex(current.Configs, childID)
	if index < 0 {
		return BoundConfig{}, false
	}
	detached := current.Configs[index]
	detached.Config = cloneBoundConfig(detached.Config)
	return detached, true
}

func boundConfigIndex(values []BoundConfig, childID session.SessionID) int {
	for index, current := range values {
		if current.ChildSessionID == childID {
			return index
		}
	}
	return -1
}

func cloneBoundCreation(source subagent.BoundCreation) subagent.BoundCreation {
	prompt, _ := agentmessage.CloneContentBlocks(source.InitialPrompt)
	detached := source
	detached.InitialPrompt = prompt
	if source.AgentOptions.MaxTokens != nil {
		maxTokens := *source.AgentOptions.MaxTokens
		detached.AgentOptions.MaxTokens = &maxTokens
	}
	return detached
}

func cloneBoundConfig(
	source subagent.BoundConfigSnapshot,
) subagent.BoundConfigSnapshot {
	detached := source
	if source.Persona != nil {
		persona := *source.Persona
		detached.Persona = &persona
	}
	if source.ToolRestriction != nil {
		detached.ToolRestriction = &tools.ToolRestriction{
			Allow: append([]string(nil), source.ToolRestriction.Allow...),
			Deny:  append([]string(nil), source.ToolRestriction.Deny...),
		}
	}
	detached.Extensions = append([]string(nil), source.Extensions...)
	return detached
}

func validSessionID(identifier session.SessionID) bool {
	return strings.TrimSpace(string(identifier)) != "" &&
		string(identifier) == strings.TrimSpace(string(identifier))
}

var _ sessionprojection.Unit = boundUnit{}
