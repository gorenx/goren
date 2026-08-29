package llm

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"

	"github.com/gorenx/goren/internal/jsonvalue"
)

func decodeStrict[T any](rawValue []byte, destination *T) error {
	if err := jsonvalue.Validate(rawValue); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(rawValue))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("llm: unexpected trailing JSON")
		}
		return err
	}
	return nil
}
