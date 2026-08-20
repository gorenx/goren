// Package configuration defines the typed JSON ingress boundary used by
// statically linked Plugin configurators. It is not part of Runtime mounting.
package configuration

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Input marks an owner-defined deployment configuration accepted by a Plugin
// Configurator. Runtime objects and business DTOs must not satisfy this marker.
type Input interface {
	PluginConfigInput()
}

// InputBase lets a named deployment configuration satisfy Input by embedding
// it. It has no JSON representation or runtime behavior.
type InputBase struct{}

// PluginConfigInput implements Input.
func (InputBase) PluginConfigInput() {}

// Document is one immutable JSON configuration value obtained by an inbound
// configuration source. It deliberately does not expose its encoded bytes.
type Document struct {
	body json.RawMessage
}

// NewDocument copies one JSON configuration value from an inbound source.
// Structural and owner-defined validation occurs during typed decoding and
// configuration, not while reading the source.
func NewDocument(source []byte) (Document, error) {
	if len(bytes.TrimSpace(source)) == 0 {
		return Document{}, errors.New("plugin configuration: document is empty")
	}
	return Document{
		body: append(json.RawMessage(nil), source...),
	}, nil
}

// DecodeJSON strictly decodes a Document into one semantic configuration
// input. It rejects duplicate fields, unknown fields, wrong types, and trailing
// JSON values. Owner defaults and cross-field validation remain the
// Configurator's responsibility.
func DecodeJSON[C Input](sourceDocument Document) (C, error) {
	var settings C
	if len(bytes.TrimSpace(sourceDocument.body)) == 0 {
		return settings, errors.New("plugin configuration: document is empty")
	}
	if err := rejectDuplicateKeys(sourceDocument.body); err != nil {
		return settings, err
	}
	decoder := json.NewDecoder(bytes.NewReader(sourceDocument.body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		return settings, fmt.Errorf("plugin configuration: invalid JSON: %w", err)
	}
	if err := ensureJSONEnd(decoder); err != nil {
		return settings, err
	}
	return settings, nil
}

func ensureJSONEnd(decoder *json.Decoder) error {
	var trailingValue json.RawMessage
	if err := decoder.Decode(&trailingValue); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("plugin configuration: document contains multiple JSON values")
		}
		return fmt.Errorf("plugin configuration: invalid trailing JSON: %w", err)
	}
	return nil
}

func rejectDuplicateKeys(source json.RawMessage) error {
	decoder := json.NewDecoder(bytes.NewReader(source))
	if err := scanJSONValue(decoder, "$"); err != nil {
		return fmt.Errorf("plugin configuration: invalid JSON: %w", err)
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
		itemIndex := 0
		for decoder.More() {
			if err := scanJSONValue(decoder, fmt.Sprintf("%s[%d]", path, itemIndex)); err != nil {
				return err
			}
			itemIndex++
		}
		_, err = decoder.Token()
		return err
	default:
		return fmt.Errorf("unexpected delimiter %q", delimiter)
	}
}
