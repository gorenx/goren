package connection

import (
	"bytes"
	"encoding/json"
)

type errorDetailRules struct {
	requiredStrings []string
	optionalStrings []string
	requiredNumbers []string
	stringArrays    []string
	rawArrays       []string
	enumField       string
	enumValues      []string
}

var rpcErrorDetailRules = map[RPCErrorCode]errorDetailRules{
	ErrorBadRequest:                 {rawArrays: []string{"issues"}},
	ErrorCancelled:                  {},
	ErrorSessionNotFound:            {requiredStrings: []string{"sessionId"}},
	ErrorModelUnavailable:           {requiredStrings: []string{"provider", "model"}},
	ErrorSessionConflict:            {requiredStrings: []string{"sessionId", "requestedCwd"}, optionalStrings: []string{"existingCwd"}},
	ErrorInvalidTimeZone:            {requiredStrings: []string{"value"}},
	ErrorWorkspaceAttachFailed:      {requiredStrings: []string{"sessionId", "workspaceId"}},
	ErrorWorkspaceNotFound:          {requiredStrings: []string{"workspaceId"}},
	ErrorWorkspaceInvalidPath:       {requiredStrings: []string{"path"}},
	ErrorWorkspaceNameConflict:      {requiredStrings: []string{"name"}},
	ErrorWorkspaceMoveInvalid:       {requiredStrings: []string{"workspaceId", "sessionId"}, optionalStrings: []string{"beforeSessionId"}},
	ErrorDirectoryUnreadable:        {requiredStrings: []string{"path"}},
	ErrorDirectoryExists:            {requiredStrings: []string{"path"}},
	ErrorDirectoryCreateFailed:      {requiredStrings: []string{"path"}},
	ErrorDirectoryPickerUnavailable: {requiredStrings: []string{"capability"}},
	ErrorAgentPresetReadOnly:        {requiredStrings: []string{"agentPreset", "reason"}},
	ErrorAgentPresetLocked:          {requiredStrings: []string{"sessionId", "agentPreset"}},
	ErrorAgentPresetConflict:        {requiredStrings: []string{"sessionId", "requestedPreset"}, optionalStrings: []string{"existingPreset"}},
	ErrorAgentPresetNotFound:        {requiredStrings: []string{"agentPreset"}, stringArrays: []string{"available"}},
	ErrorAgentPresetInvalid:         {requiredStrings: []string{"agentPreset", "reason"}},
	ErrorAgentBusy:                  {requiredStrings: []string{"reason"}},
	ErrorAttachment:                 {requiredStrings: []string{"reason"}},
	ErrorQueueItemNotFound:          {requiredStrings: []string{"itemId"}},
	ErrorSteerUnavailable:           {requiredStrings: []string{"itemId"}},
	ErrorCommand:                    {},
	ErrorUnknownCommand:             {},
	ErrorSettingsRejected:           {requiredStrings: []string{"ns"}},
	ErrorSettingsNotExposed:         {requiredStrings: []string{"ns"}},
	ErrorSettingsConflict:           {requiredStrings: []string{"ns"}, requiredNumbers: []string{"expected", "actual"}},
	ErrorCredentialRejected:         {requiredStrings: []string{"ref"}},
	ErrorModelDiscoveryFailed:       {requiredStrings: []string{"settingsNs"}, optionalStrings: []string{"baseURL"}},
	ErrorTitleInvalid:               {requiredStrings: []string{"sessionId"}},
	ErrorForkUnavailable:            {requiredStrings: []string{"sessionId"}},
	ErrorSubagentParentUnavailable:  {requiredStrings: []string{"parentSessionId"}},
	ErrorSubagentNotFound:           {requiredStrings: []string{"parentSessionId", "childSessionId"}},
	ErrorSubagentCatalogDiagnostic: {
		requiredStrings: []string{"parentSessionId", "childSessionId"},
		enumField:       "reason",
		enumValues:      []string{"corrupt", "unsupported", "unavailable"},
	},
	ErrorSubagentNotResumable:        {requiredStrings: []string{"childSessionId"}},
	ErrorSubagentUnauthorized:        {requiredStrings: []string{"childSessionId"}},
	ErrorSubagentDeliveryUnavailable: {requiredStrings: []string{"childSessionId"}},
	ErrorInternal:                    {},
}

func (rules errorDetailRules) valid(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) < 2 || trimmed[0] != '{' {
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(trimmed, &fields) != nil || fields == nil {
		return false
	}
	for _, name := range rules.requiredStrings {
		if !isJSONString(fields[name]) {
			return false
		}
	}
	for _, name := range rules.optionalStrings {
		value, exists := fields[name]
		if exists && !isJSONString(value) {
			return false
		}
	}
	for _, name := range rules.requiredNumbers {
		if !isJSONNumber(fields[name]) {
			return false
		}
	}
	for _, name := range rules.stringArrays {
		if !isStringArray(fields[name]) {
			return false
		}
	}
	for _, name := range rules.rawArrays {
		if !isJSONArray(fields[name]) {
			return false
		}
	}
	if rules.enumField != "" {
		value, ok := decodeString(fields[rules.enumField])
		if !ok || !containsString(rules.enumValues, value) {
			return false
		}
	}
	return true
}

func isJSONString(raw json.RawMessage) bool {
	_, ok := decodeString(raw)
	return ok
}

func isJSONNumber(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return false
	}
	var value float64
	return json.Unmarshal(trimmed, &value) == nil
}

func isStringArray(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) < 2 || trimmed[0] != '[' {
		return false
	}
	var values []string
	return json.Unmarshal(trimmed, &values) == nil
}

func isJSONArray(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) < 2 || trimmed[0] != '[' {
		return false
	}
	var values []json.RawMessage
	return json.Unmarshal(trimmed, &values) == nil
}

func containsString(values []string, target string) bool {
	for _, candidate := range values {
		if candidate == target {
			return true
		}
	}
	return false
}
