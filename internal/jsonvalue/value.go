// Package jsonvalue owns lossless JSON-value validation and detachment shared
// by same-process business boundaries.
package jsonvalue

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
)

// Clone validates one complete lossless JSON value and returns detached bytes.
func Clone(rawValue json.RawMessage) (json.RawMessage, error) {
	if err := Validate(rawValue); err != nil {
		return nil, err
	}
	return append(json.RawMessage(nil), rawValue...), nil
}

// Validate rejects duplicate object names, negative zero, non-finite or
// unrepresentable numbers, malformed input, and trailing JSON values.
func Validate(rawValue json.RawMessage) error {
	decoder := json.NewDecoder(bytes.NewReader(rawValue))
	decoder.UseNumber()
	if err := scanValue(decoder, "$", make(map[string]struct{})); err != nil {
		return err
	}
	return ensureEnd(decoder)
}

// IsObject reports whether rawValue is one complete lossless JSON object.
func IsObject(rawValue json.RawMessage) bool {
	if err := Validate(rawValue); err != nil {
		return false
	}
	trimmed := bytes.TrimSpace(rawValue)
	return len(trimmed) >= 2 && trimmed[0] == '{' && trimmed[len(trimmed)-1] == '}'
}

func scanValue(decoder *json.Decoder, path string, scratch map[string]struct{}) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if numeric, ok := token.(json.Number); ok {
		parsed, parseErr := numeric.Float64()
		if parseErr != nil || math.IsInf(parsed, 0) || math.IsNaN(parsed) || parsed == 0 && math.Signbit(parsed) {
			return fmt.Errorf("invalid JSON number %q at %s", numeric, path)
		}
		return nil
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return nil
	}
	switch delimiter {
	case '{':
		clear(scratch)
		for decoder.More() {
			nameToken, tokenErr := decoder.Token()
			if tokenErr != nil {
				return tokenErr
			}
			fieldName, ok := nameToken.(string)
			if !ok {
				return fmt.Errorf("object key at %s is not a string", path)
			}
			if _, exists := scratch[fieldName]; exists {
				return fmt.Errorf("duplicate field %q at %s", fieldName, path)
			}
			scratch[fieldName] = struct{}{}
			if err := scanValue(decoder, path+"."+fieldName, make(map[string]struct{})); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	case '[':
		index := 0
		for decoder.More() {
			if err := scanValue(decoder, fmt.Sprintf("%s[%d]", path, index), make(map[string]struct{})); err != nil {
				return err
			}
			index++
		}
		_, err = decoder.Token()
		return err
	default:
		return fmt.Errorf("unexpected delimiter %q", delimiter)
	}
}

func ensureEnd(decoder *json.Decoder) error {
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}
