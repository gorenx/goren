package tokenmeter

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"

	sessionprojection "github.com/gorenx/goren/session/projection"
)

func encodeProjectionState(value any) (json.RawMessage, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

func decodeProjectionState(rawValue json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(rawValue))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("tokenmeter: projection state has trailing JSON")
		}
		return err
	}
	return nil
}

func unchangedProjection(rawState json.RawMessage) sessionprojection.Transition {
	return sessionprojection.Transition{
		State:   append(json.RawMessage(nil), rawState...),
		Changed: false,
	}
}

func changedProjection(value any) (sessionprojection.Transition, error) {
	encoded, err := encodeProjectionState(value)
	if err != nil {
		return sessionprojection.Transition{}, err
	}
	return sessionprojection.Transition{
		State:   encoded,
		Changed: true,
	}, nil
}

func projectionTransition(
	previous json.RawMessage,
	next any,
) (sessionprojection.Transition, error) {
	encoded, err := encodeProjectionState(next)
	if err != nil {
		return sessionprojection.Transition{}, err
	}
	return sessionprojection.Transition{
		State:   encoded,
		Changed: !bytes.Equal(previous, encoded),
	}, nil
}
