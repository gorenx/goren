package projection

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"

	sessionprojection "github.com/gorenx/goren/session/projection"
)

// Units returns the complete ordered set of Subagent Session projections.
// Callers register the set without depending on concrete Unit types or keys.
func Units() []sessionprojection.Unit {
	return []sessionprojection.Unit{
		timingUnit{},
		identityUnit{},
	}
}

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
