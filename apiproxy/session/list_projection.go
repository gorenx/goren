package sessionapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gorenx/goren/internal/jsonvalue"
	"github.com/gorenx/goren/session"
	sessionprojection "github.com/gorenx/goren/session/projection"
)

const sessionListMetadataKey = "sessionListMetadata"

type sessionListMetadataState struct {
	Blank        bool   `json:"blank"`
	LastPromptAt *int64 `json:"lastPromptAt"`
}

type sessionListMetadataUnit struct{}

// SessionListMetadataUnit returns the API-owned projection registered by the
// Host Plugin for session.list cold summaries.
func SessionListMetadataUnit() sessionprojection.Unit {
	return sessionListMetadataUnit{}
}

func (sessionListMetadataUnit) Key() string {
	return sessionListMetadataKey
}

func (sessionListMetadataUnit) StateVersion() int64 {
	return 1
}

func (sessionListMetadataUnit) InitialState() (json.RawMessage, error) {
	return encodeSessionListMetadata(sessionListMetadataState{
		Blank: true,
	})
}

func (sessionListMetadataUnit) ApplyState(
	state json.RawMessage,
	committed session.Event,
) (sessionprojection.Transition, error) {
	current, err := decodeSessionListMetadata(state)
	if err != nil {
		return sessionprojection.Transition{}, err
	}
	changed := false
	if committed.Type == session.TurnStartEventName && current.Blank {
		current.Blank = false
		changed = true
	}
	if committed.Type == session.UserMessageEventName && directUserEvent(committed) &&
		(current.LastPromptAt == nil || committed.Time > *current.LastPromptAt) {
		promptTime := committed.Time
		current.LastPromptAt = &promptTime
		changed = true
	}
	next, err := encodeSessionListMetadata(current)
	if err != nil {
		return sessionprojection.Transition{}, err
	}
	return sessionprojection.Transition{
		State:   next,
		Changed: changed,
	}, nil
}

func (sessionListMetadataUnit) ViewState(
	state json.RawMessage,
) (json.RawMessage, error) {
	current, err := decodeSessionListMetadata(state)
	if err != nil {
		return nil, err
	}
	return encodeSessionListMetadata(current)
}

func readSessionListMetadata(
	values sessionprojection.Values,
) (sessionListMetadataState, bool, error) {
	rawValue, found := values[sessionListMetadataKey]
	if !found {
		return sessionListMetadataState{}, false, nil
	}
	state, err := decodeSessionListMetadata(rawValue)
	if err != nil {
		return sessionListMetadataState{}, false, fmt.Errorf(
			"apiproxy/session: decode %s projection: %w",
			sessionListMetadataKey,
			err,
		)
	}
	return state, true, nil
}

func encodeSessionListMetadata(
	state sessionListMetadataState,
) (json.RawMessage, error) {
	if state.LastPromptAt != nil && *state.LastPromptAt < 0 {
		return nil, errors.New("lastPromptAt must be non-negative")
	}
	rawValue, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	return jsonvalue.Clone(rawValue)
}

func decodeSessionListMetadata(
	rawValue json.RawMessage,
) (sessionListMetadataState, error) {
	if !jsonvalue.IsObject(rawValue) {
		return sessionListMetadataState{}, errors.New("value must be one plain JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(rawValue))
	decoder.DisallowUnknownFields()
	var state sessionListMetadataState
	if err := decoder.Decode(&state); err != nil {
		return sessionListMetadataState{}, err
	}
	if state.LastPromptAt != nil && *state.LastPromptAt < 0 {
		return sessionListMetadataState{}, errors.New("lastPromptAt must be non-negative")
	}
	return state, nil
}

var _ sessionprojection.Unit = sessionListMetadataUnit{}
