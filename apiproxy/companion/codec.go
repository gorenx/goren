package companionapi

import (
	"bytes"
	"encoding/json"
	"slices"

	"github.com/gorenx/goren/apiproxy/rpc"
	"github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/session"
	companions "github.com/gorenx/goren/subagent/companion"
)

// DecodeListRequest rejects model routing and unknown management fields.
func DecodeListRequest(raw json.RawMessage) (ListRequest, []connection.ValidationIssue) {
	fields, issues := managementFields(raw, "parentSessionId")
	parent, more := parentField(fields)
	return ListRequest{
		ParentSessionID: parent,
	}, append(issues, more...)
}

// DecodeCreateRequest accepts a complete typed policy or the selected named policy.
func DecodeCreateRequest(raw json.RawMessage) (CreateRequest, []connection.ValidationIssue) {
	fields, issues := managementFields(raw, "parentSessionId", "name", "config")
	parent, more := parentField(fields)
	issues = append(issues, more...)
	name, more := nameField(fields)
	issues = append(issues, more...)
	var selected *companions.Config
	if encoded, found := fields["config"]; found {
		value, err := DecodeConfig(encoded)
		if err != nil {
			issues = append(issues, rpc.InvalidType([]string{"config"}, "Companion configuration"))
		} else {
			selected = &value
		}
	}
	return CreateRequest{
		ParentSessionID: parent,
		Name:            name,
		Config:          selected,
	}, issues
}

// DecodeConfigureRequest requires a management revision and explicit target.
func DecodeConfigureRequest(raw json.RawMessage) (ConfigureRequest, []connection.ValidationIssue) {
	fields, issues := managementFields(raw, "parentSessionId", "name", "expectedRevision", "enabled")
	parent, more := parentField(fields)
	issues = append(issues, more...)
	name, more := nameField(fields)
	issues = append(issues, more...)
	revision, more := revisionField(fields)
	issues = append(issues, more...)
	var enabled bool
	if encoded, found := fields["enabled"]; !found || json.Unmarshal(encoded, &enabled) != nil || bytes.Equal(bytes.TrimSpace(encoded), []byte("null")) {
		issues = append(issues, rpc.InvalidType([]string{"enabled"}, "boolean"))
	}
	return ConfigureRequest{
		ParentSessionID:  parent,
		Name:             name,
		ExpectedRevision: revision,
		Enabled:          enabled,
	}, issues
}

// DecodeRemoveRequest requires the observed relationship revision.
func DecodeRemoveRequest(raw json.RawMessage) (RemoveRequest, []connection.ValidationIssue) {
	fields, issues := managementFields(raw, "parentSessionId", "name", "expectedRevision")
	parent, more := parentField(fields)
	issues = append(issues, more...)
	name, more := nameField(fields)
	issues = append(issues, more...)
	revision, more := revisionField(fields)
	return RemoveRequest{
		ParentSessionID:  parent,
		Name:             name,
		ExpectedRevision: revision,
	}, append(issues, more...)
}

// DecodeRestartRequest also fences the child creation observed by the caller.
func DecodeRestartRequest(raw json.RawMessage) (RestartRequest, []connection.ValidationIssue) {
	fields, issues := managementFields(raw, "parentSessionId", "name", "expectedRevision", "expectedCreationId")
	parent, more := parentField(fields)
	issues = append(issues, more...)
	name, more := nameField(fields)
	issues = append(issues, more...)
	revision, more := revisionField(fields)
	issues = append(issues, more...)
	lifetime, more := rpc.RequiredString(fields, "expectedCreationId", false)
	return RestartRequest{
		ParentSessionID:    parent,
		Name:               name,
		ExpectedRevision:   revision,
		ExpectedCreationID: lifetime,
	}, append(issues, more...)
}

func managementFields(raw json.RawMessage, allowed ...string) (map[string]json.RawMessage, []connection.ValidationIssue) {
	fields, issues := rpc.ObjectFields(raw)
	for name := range fields {
		if !slices.Contains(allowed, name) {
			issues = append(issues, connection.ValidationIssue{
				Code:    "unrecognized_key",
				Path:    []string{name},
				Message: "Unknown Companion management field",
			})
		}
	}
	return fields, issues
}

func parentField(fields map[string]json.RawMessage) (session.SessionID, []connection.ValidationIssue) {
	value, issues := rpc.RequiredString(fields, "parentSessionId", true)
	return session.SessionID(value), issues
}

func nameField(fields map[string]json.RawMessage) (string, []connection.ValidationIssue) {
	value, issues := rpc.RequiredString(fields, "name", true)
	if len(issues) == 0 && companions.ValidateName(value) != nil {
		issues = append(issues, rpc.InvalidType([]string{"name"}, "Companion name"))
	}
	return value, issues
}

func revisionField(fields map[string]json.RawMessage) (int64, []connection.ValidationIssue) {
	encoded, found := fields["expectedRevision"]
	var revision int64
	if !found || json.Unmarshal(encoded, &revision) != nil || revision < 1 {
		return 0, []connection.ValidationIssue{rpc.InvalidType([]string{"expectedRevision"}, "positive integer")}
	}
	return revision, nil
}
