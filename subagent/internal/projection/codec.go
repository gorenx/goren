package projection

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

func decodeJSON(rawValue json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(rawValue))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("subagent: projection contains multiple JSON values")
		}
		return err
	}
	return nil
}
