package plugin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// DecodeStrictConfig decodes one JSON object into a named Go config type,
// rejects unknown and duplicate fields, then applies owner-defined validation.
func DecodeStrictConfig[C any](rawConfig json.RawMessage, configCheck func(C) error) (C, error) {
	var typedConfig C
	if len(bytes.TrimSpace(rawConfig)) == 0 {
		return typedConfig, errors.New("plugin: config is empty")
	}
	if err := rejectDuplicateKeys(rawConfig); err != nil {
		return typedConfig, err
	}
	decoder := json.NewDecoder(bytes.NewReader(rawConfig))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&typedConfig); err != nil {
		return typedConfig, fmt.Errorf("plugin: invalid config: %w", err)
	}
	if err := ensureJSONEnd(decoder); err != nil {
		return typedConfig, err
	}
	if configCheck != nil {
		if err := configCheck(typedConfig); err != nil {
			return typedConfig, fmt.Errorf("plugin: invalid config: %w", err)
		}
	}
	return typedConfig, nil
}

func ensureJSONEnd(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("plugin: config contains multiple JSON values")
		}
		return fmt.Errorf("plugin: invalid trailing config: %w", err)
	}
	return nil
}

func rejectDuplicateKeys(rawConfig json.RawMessage) error {
	decoder := json.NewDecoder(bytes.NewReader(rawConfig))
	if err := scanJSONValue(decoder, "$"); err != nil {
		return fmt.Errorf("plugin: invalid config: %w", err)
	}
	return ensureJSONEnd(decoder)
}

func scanJSONValue(decoder *json.Decoder, path string) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			nameToken, tokenErr := decoder.Token()
			if tokenErr != nil {
				return tokenErr
			}
			fieldName, ok := nameToken.(string)
			if !ok {
				return fmt.Errorf("object key at %s is not a string", path)
			}
			if _, exists := seen[fieldName]; exists {
				return fmt.Errorf("duplicate field %q at %s", fieldName, path)
			}
			seen[fieldName] = struct{}{}
			if err := scanJSONValue(decoder, path+"."+fieldName); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	case '[':
		index := 0
		for decoder.More() {
			if err := scanJSONValue(decoder, fmt.Sprintf("%s[%d]", path, index)); err != nil {
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
