package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

// ValidateCreateContext rejects an absent or already-cancelled Factory
// construction Context before domain decoding or Plugin allocation begins.
func ValidateCreateContext(createContext context.Context) error {
	if createContext == nil {
		return errors.New("plugin factory: create Context is nil")
	}
	return createContext.Err()
}

// ValidateObjectConfig verifies the common transport contract for typed Plugin
// configuration. Domain Factories remain responsible for decoding fields into
// their named Config type and validating their meaning.
func ValidateObjectConfig(
	rawConfig json.RawMessage,
	configurationOwner string,
) error {
	if err := validateConfigurationOwner(configurationOwner); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(rawConfig))
	decoder.UseNumber()
	firstToken, err := decoder.Token()
	if err != nil {
		return invalidConfiguration(configurationOwner, err)
	}
	opening, objectStart := firstToken.(json.Delim)
	if !objectStart || opening != '{' {
		return fmt.Errorf(
			"%s: configuration must be a JSON object",
			configurationOwner,
		)
	}
	if err = scanConfigurationObject(decoder); err != nil {
		return invalidConfiguration(configurationOwner, err)
	}
	var trailingValue json.RawMessage
	if err = decoder.Decode(&trailingValue); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return invalidConfiguration(configurationOwner, err)
	}
	return nil
}

// ValidateEmptyConfig accepts exactly one empty JSON object for a Factory that
// has no configuration vocabulary. It rejects every invented setting instead
// of introducing an empty Config type and a meaningless decode function.
func ValidateEmptyConfig(
	rawConfig json.RawMessage,
	configurationOwner string,
) error {
	if err := ValidateObjectConfig(rawConfig, configurationOwner); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(rawConfig, &fields); err != nil {
		return invalidConfiguration(configurationOwner, err)
	}
	if len(fields) == 0 {
		return nil
	}
	fieldNames := make([]string, 0, len(fields))
	for fieldName := range fields {
		fieldNames = append(fieldNames, fieldName)
	}
	sort.Strings(fieldNames)
	return fmt.Errorf(
		"%s: unknown field %q",
		configurationOwner,
		fieldNames[0],
	)
}

func scanConfigurationObject(decoder *json.Decoder) error {
	seenFields := make(map[string]struct{})
	for decoder.More() {
		fieldToken, err := decoder.Token()
		if err != nil {
			return err
		}
		fieldName, matches := fieldToken.(string)
		if !matches {
			return errors.New("configuration field name must be a string")
		}
		if _, repeated := seenFields[fieldName]; repeated {
			return fmt.Errorf("duplicate field %q", fieldName)
		}
		seenFields[fieldName] = struct{}{}
		valueToken, tokenErr := decoder.Token()
		if tokenErr != nil {
			return tokenErr
		}
		if err = scanConfigurationValue(decoder, valueToken); err != nil {
			return err
		}
	}
	closingToken, err := decoder.Token()
	if err != nil {
		return err
	}
	closing, matches := closingToken.(json.Delim)
	if !matches || closing != '}' {
		return errors.New("configuration object is not closed")
	}
	return nil
}

func scanConfigurationValue(decoder *json.Decoder, token json.Token) error {
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	switch delimiter {
	case '{':
		return scanConfigurationObject(decoder)
	case '[':
		for decoder.More() {
			itemToken, err := decoder.Token()
			if err != nil {
				return err
			}
			if err = scanConfigurationValue(decoder, itemToken); err != nil {
				return err
			}
		}
		closingToken, err := decoder.Token()
		if err != nil {
			return err
		}
		closing, matches := closingToken.(json.Delim)
		if !matches || closing != ']' {
			return errors.New("configuration array is not closed")
		}
		return nil
	default:
		return errors.New("unexpected configuration delimiter")
	}
}

func validateConfigurationOwner(configurationOwner string) error {
	if strings.TrimSpace(configurationOwner) == "" ||
		configurationOwner != strings.TrimSpace(configurationOwner) {
		return errors.New(
			"plugin factory: configuration owner must be non-empty and trimmed",
		)
	}
	return nil
}

func invalidConfiguration(configurationOwner string, cause error) error {
	return fmt.Errorf(
		"%s: invalid configuration: %w",
		configurationOwner,
		cause,
	)
}
