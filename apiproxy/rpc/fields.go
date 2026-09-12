package rpc

import (
	"bytes"
	"encoding/json"

	"github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/internal/jsonvalue"
)

// ObjectFields reads an object payload without imposing domain field rules.
func ObjectFields(rawPayload json.RawMessage) (map[string]json.RawMessage, []connection.ValidationIssue) {
	if !jsonvalue.IsObject(rawPayload) {
		return nil, []connection.ValidationIssue{InvalidType(nil, "object")}
	}
	fields := make(map[string]json.RawMessage)
	if err := json.Unmarshal(rawPayload, &fields); err != nil {
		return nil, []connection.ValidationIssue{InvalidType(nil, "object")}
	}
	return fields, nil
}

// RequiredString validates presence, JSON type and optional nonempty constraint.
func RequiredString(fields map[string]json.RawMessage, name string, nonempty bool) (string, []connection.ValidationIssue) {
	rawValue, exists := fields[name]
	if !exists {
		return "", []connection.ValidationIssue{InvalidType([]string{name}, "string")}
	}
	var textValue string
	if bytes.Equal(bytes.TrimSpace(rawValue), []byte("null")) || json.Unmarshal(rawValue, &textValue) != nil {
		return "", []connection.ValidationIssue{InvalidType([]string{name}, "string")}
	}
	if nonempty && textValue == "" {
		return "", []connection.ValidationIssue{{
			Code:    "too_small",
			Path:    []string{name},
			Message: "Too small: expected string to have >=1 characters",
		}}
	}
	return textValue, nil
}

// InvalidType reports a JSON type mismatch at the supplied field path.
func InvalidType(path []string, expected string) connection.ValidationIssue {
	return connection.ValidationIssue{
		Code:     "invalid_type",
		Expected: expected,
		Path:     path,
		Message:  "Invalid input: expected " + expected,
	}
}

// OptionalString accepts absence but validates any supplied string.
func OptionalString(fields map[string]json.RawMessage, name string, nonempty bool) (*string, []connection.ValidationIssue) {
	if _, exists := fields[name]; !exists {
		return nil, nil
	}
	textValue, issues := RequiredString(fields, name, nonempty)
	if len(issues) != 0 {
		return nil, issues
	}
	return &textValue, nil
}

// PrefixIssues returns issues with detached paths under a containing field.
func PrefixIssues(issues []connection.ValidationIssue, prefix ...string) []connection.ValidationIssue {
	result := make([]connection.ValidationIssue, len(issues))
	for index, issue := range issues {
		path := make([]string, 0, len(prefix)+len(issue.Path))
		path = append(path, prefix...)
		path = append(path, issue.Path...)
		issue.Path = path
		result[index] = issue
	}
	return result
}
