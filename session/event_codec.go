package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

func decodeSessionPayload[T any](rawValue json.RawMessage, target *T) error {
	decoder := json.NewDecoder(bytes.NewReader(rawValue))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("session: event payload contains multiple JSON values")
		}
		return err
	}
	return nil
}
