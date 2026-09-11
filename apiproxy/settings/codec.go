package settingsapi

import (
	"bytes"
	"encoding/json"

	"github.com/gorenx/goren/apiproxy/rpc"
	"github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/internal/jsonvalue"
	"github.com/gorenx/goren/settings"
)

// DecodeUpdateProviderRequest accepts only a complete named group.
func DecodeUpdateProviderRequest(encoded json.RawMessage) (UpdateProviderRequest, []connection.ValidationIssue) {
	revision, fields, valid := requestFields(encoded, "name", "config")
	if !valid {
		return UpdateProviderRequest{}, invalidSettingsRequest()
	}
	name, issues := rpc.RequiredString(fields, "name", true)
	if len(issues) != 0 {
		return UpdateProviderRequest{}, invalidSettingsRequest()
	}
	configuration, err := settings.DecodeProvider(fields["config"])
	if err != nil {
		return UpdateProviderRequest{}, invalidSettingsRequest()
	}
	return UpdateProviderRequest{
		Name:             name,
		ExpectedRevision: revision,
		Config:           configuration,
	}, nil
}

// DecodeResetProviderRequest accepts the complete group identity and revision.
func DecodeResetProviderRequest(encoded json.RawMessage) (ResetProviderRequest, []connection.ValidationIssue) {
	revision, fields, valid := requestFields(encoded, "name")
	if !valid {
		return ResetProviderRequest{}, invalidSettingsRequest()
	}
	name, issues := rpc.RequiredString(fields, "name", true)
	if len(issues) != 0 {
		return ResetProviderRequest{}, invalidSettingsRequest()
	}
	return ResetProviderRequest{
		Name:             name,
		ExpectedRevision: revision,
	}, nil
}

// DecodeUpdateToolRequest accepts only a complete named group.
func DecodeUpdateToolRequest(encoded json.RawMessage) (UpdateToolRequest, []connection.ValidationIssue) {
	revision, fields, valid := requestFields(encoded, "name", "config")
	if !valid {
		return UpdateToolRequest{}, invalidSettingsRequest()
	}
	name, issues := rpc.RequiredString(fields, "name", true)
	if len(issues) != 0 {
		return UpdateToolRequest{}, invalidSettingsRequest()
	}
	configuration, err := settings.DecodeTool(fields["config"])
	if err != nil {
		return UpdateToolRequest{}, invalidSettingsRequest()
	}
	return UpdateToolRequest{
		Name:             name,
		ExpectedRevision: revision,
		Config:           configuration,
	}, nil
}

// DecodeResetToolRequest accepts the complete group identity and revision.
func DecodeResetToolRequest(encoded json.RawMessage) (ResetToolRequest, []connection.ValidationIssue) {
	revision, fields, valid := requestFields(encoded, "name")
	if !valid {
		return ResetToolRequest{}, invalidSettingsRequest()
	}
	name, issues := rpc.RequiredString(fields, "name", true)
	if len(issues) != 0 {
		return ResetToolRequest{}, invalidSettingsRequest()
	}
	return ResetToolRequest{
		Name:             name,
		ExpectedRevision: revision,
	}, nil
}

// DecodeUpdatePromptRequest accepts only a complete named group.
func DecodeUpdatePromptRequest(encoded json.RawMessage) (UpdatePromptRequest, []connection.ValidationIssue) {
	revision, fields, valid := requestFields(encoded, "name", "config")
	if !valid {
		return UpdatePromptRequest{}, invalidSettingsRequest()
	}
	name, issues := rpc.RequiredString(fields, "name", true)
	if len(issues) != 0 {
		return UpdatePromptRequest{}, invalidSettingsRequest()
	}
	configuration, err := settings.DecodePrompt(fields["config"])
	if err != nil {
		return UpdatePromptRequest{}, invalidSettingsRequest()
	}
	return UpdatePromptRequest{
		Name:             name,
		ExpectedRevision: revision,
		Config:           configuration,
	}, nil
}

// DecodeResetPromptRequest accepts the complete group identity and revision.
func DecodeResetPromptRequest(encoded json.RawMessage) (ResetPromptRequest, []connection.ValidationIssue) {
	revision, fields, valid := requestFields(encoded, "name")
	if !valid {
		return ResetPromptRequest{}, invalidSettingsRequest()
	}
	name, issues := rpc.RequiredString(fields, "name", true)
	if len(issues) != 0 {
		return ResetPromptRequest{}, invalidSettingsRequest()
	}
	return ResetPromptRequest{
		Name:             name,
		ExpectedRevision: revision,
	}, nil
}

// DecodeUpdateMCPRequest accepts only a complete named group.
func DecodeUpdateMCPRequest(encoded json.RawMessage) (UpdateMCPRequest, []connection.ValidationIssue) {
	revision, fields, valid := requestFields(encoded, "name", "config")
	if !valid {
		return UpdateMCPRequest{}, invalidSettingsRequest()
	}
	name, issues := rpc.RequiredString(fields, "name", true)
	if len(issues) != 0 {
		return UpdateMCPRequest{}, invalidSettingsRequest()
	}
	configuration, err := settings.DecodeMCP(fields["config"])
	if err != nil {
		return UpdateMCPRequest{}, invalidSettingsRequest()
	}
	if configuration.Validate(name) != nil {
		return UpdateMCPRequest{}, invalidSettingsRequest()
	}
	return UpdateMCPRequest{
		Name:             name,
		ExpectedRevision: revision,
		Config:           configuration,
	}, nil
}

// DecodeResetMCPRequest accepts the complete group identity and revision.
func DecodeResetMCPRequest(encoded json.RawMessage) (ResetMCPRequest, []connection.ValidationIssue) {
	revision, fields, valid := requestFields(encoded, "name")
	if !valid {
		return ResetMCPRequest{}, invalidSettingsRequest()
	}
	name, issues := rpc.RequiredString(fields, "name", true)
	if len(issues) != 0 {
		return ResetMCPRequest{}, invalidSettingsRequest()
	}
	return ResetMCPRequest{
		Name:             name,
		ExpectedRevision: revision,
	}, nil
}

// DecodeRetryApplicationRequest requires capability, name and saved revision.
func DecodeRetryApplicationRequest(encoded json.RawMessage) (RetryApplicationRequest, []connection.ValidationIssue) {
	revision, fields, valid := requestFields(encoded, "ns", "name")
	if !valid {
		return RetryApplicationRequest{}, invalidSettingsRequest()
	}
	namespace, issues := rpc.RequiredString(fields, "ns", true)
	if len(issues) != 0 {
		return RetryApplicationRequest{}, invalidSettingsRequest()
	}
	name, issues := rpc.RequiredString(fields, "name", true)
	if len(issues) != 0 {
		return RetryApplicationRequest{}, invalidSettingsRequest()
	}
	return RetryApplicationRequest{
		NS:               namespace,
		Name:             name,
		ExpectedRevision: revision,
	}, nil
}

// requestFields checks exact envelope members and a safe integer revision.
func requestFields(encoded json.RawMessage, members ...string) (int64, map[string]json.RawMessage, bool) {
	if !jsonvalue.IsObject(encoded) {
		return 0, nil, false
	}
	fields, _ := rpc.ObjectFields(encoded)
	if len(fields) != len(members)+1 {
		return 0, nil, false
	}
	for _, member := range members {
		if _, found := fields[member]; !found {
			return 0, nil, false
		}
	}
	rawRevision, found := fields["expectedRevision"]
	if !found || bytes.Equal(bytes.TrimSpace(rawRevision), []byte("null")) {
		return 0, nil, false
	}
	var revision int64
	if json.Unmarshal(rawRevision, &revision) != nil ||
		revision < 0 ||
		revision > 9_007_199_254_740_991 {
		return 0, nil, false
	}
	return revision, fields, true
}

func invalidSettingsRequest() []connection.ValidationIssue {
	return []connection.ValidationIssue{{
		Code:    "invalid_settings_request",
		Path:    []string{},
		Message: "Invalid settings request",
	}}
}
