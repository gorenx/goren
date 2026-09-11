// Package file implements strict local configuration input. Untyped values live
// only here, before decoding into the configuration owner's named structs.
package file

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/gorenx/goren/internal/jsonvalue"
	jsonparser "github.com/knadh/koanf/parsers/json"
	yamlparser "github.com/knadh/koanf/parsers/yaml"
	fileprovider "github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
	"go.yaml.in/yaml/v3"
)

// ErrInvalid deliberately excludes user-supplied keys, values and parser errors.
var ErrInvalid = errors.New("settings: invalid configuration file")

// Read reads one YAML or JSON document into a named configuration struct.
func Read(path string, destination any) error {
	data, err := fileprovider.Provider(path).ReadBytes()
	if err != nil {
		return errors.New("settings: cannot read configuration file")
	}
	return Decode(data, filepath.Ext(path), destination)
}

// Decode checks both the source representation and canonical JSON field names.
func Decode(data []byte, extension string, destination any) error {
	shape := reflect.TypeOf(destination)
	if shape == nil || shape.Kind() != reflect.Pointer {
		return ErrInvalid
	}
	var parser koanf.Parser
	switch strings.ToLower(extension) {
	case ".json":
		if !jsonvalue.IsObject(data) {
			return ErrInvalid
		}
		// Validate before the framework converts JSON numbers to float64.
		if err := validateFields(data, shape); err != nil {
			return err
		}
		parser = jsonparser.Parser()
	case "", ".yaml", ".yml":
		decoder := yaml.NewDecoder(bytes.NewReader(data))
		var document yaml.Node
		if decoder.Decode(&document) != nil || len(document.Content) != 1 {
			return ErrInvalid
		}
		if decoder.Decode(new(yaml.Node)) != io.EOF {
			return ErrInvalid
		}
		if err := validateNode(document.Content[0], shape); err != nil {
			return err
		}
		parser = yamlparser.Parser()
	default:
		return ErrInvalid
	}
	fields, err := parser.Unmarshal(data)
	if err != nil {
		return ErrInvalid
	}
	encoded, err := json.Marshal(fields)
	if err != nil || !jsonvalue.IsObject(encoded) {
		return ErrInvalid
	}
	return decodeTyped(encoded, destination)
}

// validateNode checks restrictions absent from the framework parser. It does
// not decode or construct values; the official YAML parser owns that operation.
func validateNode(input *yaml.Node, shape reflect.Type) error {
	if input.Anchor != "" ||
		input.Alias != nil ||
		input.Style&yaml.TaggedStyle != 0 {
		return ErrInvalid
	}
	for shape.Kind() == reflect.Pointer {
		shape = shape.Elem()
	}
	switch shape.Kind() {
	case reflect.Struct, reflect.Map:
		if input.Kind != yaml.MappingNode {
			return ErrInvalid
		}
		for index := 0; index < len(input.Content); index += 2 {
			key := input.Content[index]
			if key.Kind != yaml.ScalarNode ||
				key.Tag != "!!str" ||
				key.Anchor != "" ||
				key.Style&yaml.TaggedStyle != 0 {
				return ErrInvalid
			}
			if shape.Kind() == reflect.Map {
				if shape.Key().Kind() != reflect.String {
					return ErrInvalid
				}
				if err := validateNode(input.Content[index+1], shape.Elem()); err != nil {
					return err
				}
				continue
			}
			var fieldType reflect.Type
			for position := 0; position < shape.NumField(); position++ {
				field := shape.Field(position)
				if strings.Split(field.Tag.Get("json"), ",")[0] == key.Value {
					fieldType = field.Type
					break
				}
			}
			if fieldType == nil {
				return ErrInvalid
			}
			if err := validateNode(input.Content[index+1], fieldType); err != nil {
				return err
			}
		}
	case reflect.Slice:
		if input.Kind != yaml.SequenceNode {
			return ErrInvalid
		}
		for _, child := range input.Content {
			if err := validateNode(child, shape.Elem()); err != nil {
				return err
			}
		}
	default:
		if input.Kind != yaml.ScalarNode {
			return ErrInvalid
		}
		switch shape.Kind() {
		case reflect.String:
			if input.Tag != "!!str" {
				return ErrInvalid
			}
		case reflect.Bool:
			if input.Tag != "!!bool" || (input.Value != "true" && input.Value != "false") {
				return ErrInvalid
			}
		case reflect.Int, reflect.Int64, reflect.Uint64:
			if input.Tag != "!!int" {
				return ErrInvalid
			}
		case reflect.Float64:
			if input.Tag != "!!int" && input.Tag != "!!float" {
				return ErrInvalid
			}
		default:
			return ErrInvalid
		}
	}
	return nil
}

func decodeTyped(encoded []byte, destination any) error {
	shape := reflect.TypeOf(destination)
	if shape == nil || shape.Kind() != reflect.Pointer {
		return ErrInvalid
	}
	if reflect.ValueOf(destination).IsNil() {
		return ErrInvalid
	}
	if err := validateFields(encoded, shape); err != nil {
		return err
	}
	if json.Unmarshal(encoded, destination) != nil {
		return ErrInvalid
	}
	return nil
}

func validateFields(encoded json.RawMessage, shape reflect.Type) error {
	if bytes.Equal(bytes.TrimSpace(encoded), []byte("null")) {
		return ErrInvalid
	}
	for shape.Kind() == reflect.Pointer {
		shape = shape.Elem()
	}
	switch shape.Kind() {
	case reflect.Struct:
		var fields map[string]json.RawMessage
		if json.Unmarshal(encoded, &fields) != nil || fields == nil {
			return ErrInvalid
		}
		for key, value := range fields {
			var fieldType reflect.Type
			for index := 0; index < shape.NumField(); index++ {
				field := shape.Field(index)
				if strings.Split(field.Tag.Get("json"), ",")[0] == key {
					fieldType = field.Type
					break
				}
			}
			if fieldType == nil {
				return ErrInvalid
			}
			if err := validateFields(value, fieldType); err != nil {
				return err
			}
		}
	case reflect.Map:
		if shape.Key().Kind() != reflect.String {
			return ErrInvalid
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(encoded, &fields) != nil || fields == nil {
			return ErrInvalid
		}
		for _, value := range fields {
			if err := validateFields(value, shape.Elem()); err != nil {
				return err
			}
		}
	case reflect.Slice:
		var entries []json.RawMessage
		if json.Unmarshal(encoded, &entries) != nil {
			return ErrInvalid
		}
		for _, value := range entries {
			if err := validateFields(value, shape.Elem()); err != nil {
				return err
			}
		}
	default:
		if json.Unmarshal(encoded, reflect.New(shape).Interface()) != nil {
			return ErrInvalid
		}
	}
	return nil
}
