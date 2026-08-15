package apiproxy

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/gorenx/goren/connection"
)

const maxJavaScriptSafeInteger = int64(1<<53 - 1)

// DecodeSessionListRequest validates the source session.list payload schema.
func DecodeSessionListRequest(rawPayload json.RawMessage) (SessionListRequest, []connection.ValidationIssue) {
	fields, issues := decodeRequestObject(rawPayload)
	if len(issues) != 0 {
		return SessionListRequest{}, issues
	}
	cursor, fieldIssues := optionalStringField(fields, "cursor", false)
	return SessionListRequest{Cursor: cursor}, fieldIssues
}

// DecodeSessionCreateRequest validates the source session.create payload schema.
func DecodeSessionCreateRequest(rawPayload json.RawMessage) (SessionCreateRequest, []connection.ValidationIssue) {
	fields, issues := decodeRequestObject(rawPayload)
	if len(issues) != 0 {
		return SessionCreateRequest{}, issues
	}
	workspaceValue, workspaceIssues := optionalStringField(fields, "workspaceId", true)
	cwd, cwdIssues := optionalStringField(fields, "cwd", false)
	identifierValue, identifierIssues := optionalStringField(fields, "sessionId", true)
	preset, presetIssues := optionalStringField(fields, "agentPreset", false)
	issues = append(issues, workspaceIssues...)
	issues = append(issues, cwdIssues...)
	issues = append(issues, identifierIssues...)
	issues = append(issues, presetIssues...)
	if workspaceValue != nil && cwd != nil {
		issues = append(issues, connection.ValidationIssue{
			Code: "custom", Path: []string{}, Message: "session.create accepts workspaceId or cwd, not both",
		})
	}
	if len(issues) != 0 {
		return SessionCreateRequest{}, issues
	}
	var workspaceKey *WorkspaceID
	if workspaceValue != nil {
		converted := WorkspaceID(*workspaceValue)
		workspaceKey = &converted
	}
	var identifier *SessionID
	if identifierValue != nil {
		converted := SessionID(*identifierValue)
		identifier = &converted
	}
	return SessionCreateRequest{
		WorkspaceID: workspaceKey, CWD: cwd, SessionID: identifier, AgentPreset: preset,
	}, nil
}

// DecodeSessionHistoryRequest validates the source session.history payload schema.
func DecodeSessionHistoryRequest(rawPayload json.RawMessage) (SessionHistoryRequest, []connection.ValidationIssue) {
	fields, issues := decodeRequestObject(rawPayload)
	if len(issues) != 0 {
		return SessionHistoryRequest{}, issues
	}
	identifier, identifierIssues := requiredStringField(fields, "sessionId", true)
	beforeSeq, beforeIssues := optionalSafeIntegerField(fields, "beforeSeq", false)
	maxMessages, maxIssues := optionalSafeIntegerField(fields, "maxMessages", true)
	issues = append(issues, identifierIssues...)
	issues = append(issues, beforeIssues...)
	issues = append(issues, maxIssues...)
	return SessionHistoryRequest{
		SessionID: SessionID(identifier), BeforeSeq: beforeSeq, MaxMessages: maxMessages,
	}, issues
}

// DecodeSessionModelsRequest validates the source session.models payload schema.
func DecodeSessionModelsRequest(rawPayload json.RawMessage) (SessionModelsRequest, []connection.ValidationIssue) {
	fields, issues := decodeRequestObject(rawPayload)
	if len(issues) != 0 {
		return SessionModelsRequest{}, issues
	}
	identifier, fieldIssues := requiredStringField(fields, "sessionId", true)
	return SessionModelsRequest{SessionID: SessionID(identifier)}, fieldIssues
}

// DecodeSessionSelectModelRequest validates the source session.selectModel payload schema.
func DecodeSessionSelectModelRequest(rawPayload json.RawMessage) (SessionSelectModelRequest, []connection.ValidationIssue) {
	fields, issues := decodeRequestObject(rawPayload)
	if len(issues) != 0 {
		return SessionSelectModelRequest{}, issues
	}
	identifier, identifierIssues := requiredStringField(fields, "sessionId", true)
	provider, providerIssues := requiredStringField(fields, "provider", true)
	modelID, modelIssues := requiredStringField(fields, "model", true)
	effort, effortIssues := optionalStringField(fields, "reasoningEffort", true)
	issues = append(issues, identifierIssues...)
	issues = append(issues, providerIssues...)
	issues = append(issues, modelIssues...)
	issues = append(issues, effortIssues...)
	return SessionSelectModelRequest{
		SessionID: SessionID(identifier), Provider: provider, Model: modelID, ReasoningEffort: effort,
	}, issues
}

// DecodeSessionPromptRequest validates the source session.prompt payload schema.
func DecodeSessionPromptRequest(rawPayload json.RawMessage) (SessionPromptRequest, []connection.ValidationIssue) {
	fields, issues := decodeRequestObject(rawPayload)
	if len(issues) != 0 {
		return SessionPromptRequest{}, issues
	}
	identifier, identifierIssues := requiredStringField(fields, "sessionId", true)
	mode, modeIssues := requiredStringField(fields, "mode", false)
	zone, zoneIssues := optionalStringField(fields, "clientTimeZone", false)
	issues = append(issues, identifierIssues...)
	issues = append(issues, modeIssues...)
	issues = append(issues, zoneIssues...)
	if mode != "queue" && mode != "steer" {
		issues = append(issues, invalidValueIssue([]string{"mode"}, "Invalid option: expected one of \"queue\"|\"steer\""))
	}
	parts, contentIssues := decodePromptParts(fields["content"])
	issues = append(issues, contentIssues...)
	return SessionPromptRequest{
		SessionID: SessionID(identifier), Mode: mode, Content: parts, ClientTimeZone: zone,
	}, issues
}

// DecodeSessionUpdateQueueRequest validates the source session.updateQueue payload schema.
func DecodeSessionUpdateQueueRequest(rawPayload json.RawMessage) (SessionUpdateQueueRequest, []connection.ValidationIssue) {
	fields, issues := decodeRequestObject(rawPayload)
	if len(issues) != 0 {
		return SessionUpdateQueueRequest{}, issues
	}
	identifier, identifierIssues := requiredStringField(fields, "sessionId", true)
	itemID, itemIssues := requiredStringField(fields, "itemId", true)
	action, actionIssues := decodeQueueAction(fields["action"])
	issues = append(issues, identifierIssues...)
	issues = append(issues, itemIssues...)
	issues = append(issues, actionIssues...)
	return SessionUpdateQueueRequest{
		SessionID: SessionID(identifier), ItemID: MessageID(itemID), Action: action,
	}, issues
}

// DecodeSessionCancelRequest validates the source session.cancel payload schema.
func DecodeSessionCancelRequest(rawPayload json.RawMessage) (SessionCancelRequest, []connection.ValidationIssue) {
	fields, issues := decodeRequestObject(rawPayload)
	if len(issues) != 0 {
		return SessionCancelRequest{}, issues
	}
	identifier, fieldIssues := requiredStringField(fields, "sessionId", true)
	return SessionCancelRequest{SessionID: SessionID(identifier)}, fieldIssues
}

func decodeRequestObject(rawPayload json.RawMessage) (map[string]json.RawMessage, []connection.ValidationIssue) {
	trimmed := bytes.TrimSpace(rawPayload)
	if len(trimmed) < 2 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' {
		return nil, []connection.ValidationIssue{invalidTypeIssue(nil, "object")}
	}
	fields := make(map[string]json.RawMessage)
	if err := json.Unmarshal(trimmed, &fields); err != nil {
		return nil, []connection.ValidationIssue{invalidTypeIssue(nil, "object")}
	}
	return fields, nil
}

func requiredStringField(fields map[string]json.RawMessage, name string, nonempty bool) (string, []connection.ValidationIssue) {
	rawValue, exists := fields[name]
	if !exists {
		return "", []connection.ValidationIssue{invalidTypeIssue([]string{name}, "string")}
	}
	var textValue string
	if bytes.Equal(bytes.TrimSpace(rawValue), []byte("null")) || json.Unmarshal(rawValue, &textValue) != nil {
		return "", []connection.ValidationIssue{invalidTypeIssue([]string{name}, "string")}
	}
	if nonempty && textValue == "" {
		return "", []connection.ValidationIssue{{
			Code: "too_small", Path: []string{name}, Message: "Too small: expected string to have >=1 characters",
		}}
	}
	return textValue, nil
}

func optionalStringField(fields map[string]json.RawMessage, name string, nonempty bool) (*string, []connection.ValidationIssue) {
	if _, exists := fields[name]; !exists {
		return nil, nil
	}
	textValue, issues := requiredStringField(fields, name, nonempty)
	if len(issues) != 0 {
		return nil, issues
	}
	return &textValue, nil
}

func optionalSafeIntegerField(fields map[string]json.RawMessage, name string, positive bool) (*int64, []connection.ValidationIssue) {
	rawValue, exists := fields[name]
	if !exists {
		return nil, nil
	}
	if bytes.Equal(bytes.TrimSpace(rawValue), []byte("null")) {
		return nil, []connection.ValidationIssue{invalidTypeIssue([]string{name}, "number")}
	}
	var numberValue json.Number
	decoder := json.NewDecoder(bytes.NewReader(rawValue))
	decoder.UseNumber()
	if err := decoder.Decode(&numberValue); err != nil {
		return nil, []connection.ValidationIssue{invalidTypeIssue([]string{name}, "number")}
	}
	integerValue, err := numberValue.Int64()
	if err != nil || integerValue < 0 || integerValue > maxJavaScriptSafeInteger || positive && integerValue == 0 {
		return nil, []connection.ValidationIssue{{
			Code: "invalid_type", Expected: "integer", Path: []string{name}, Message: "Invalid input: expected integer",
		}}
	}
	return &integerValue, nil
}

func decodePromptParts(rawValue json.RawMessage) ([]PromptContentPart, []connection.ValidationIssue) {
	if len(rawValue) == 0 || bytes.Equal(bytes.TrimSpace(rawValue), []byte("null")) {
		return nil, []connection.ValidationIssue{invalidTypeIssue([]string{"content"}, "array")}
	}
	var encoded []json.RawMessage
	if err := json.Unmarshal(rawValue, &encoded); err != nil {
		return nil, []connection.ValidationIssue{invalidTypeIssue([]string{"content"}, "array")}
	}
	parts := make([]PromptContentPart, 0, len(encoded))
	issues := make([]connection.ValidationIssue, 0)
	for index, encodedPart := range encoded {
		var fields map[string]json.RawMessage
		if !json.Valid(encodedPart) || json.Unmarshal(encodedPart, &fields) != nil || fields == nil {
			issues = append(issues, invalidTypeIssue([]string{"content", fmt.Sprint(index)}, "object"))
			continue
		}
		typeName, typeIssues := requiredStringField(fields, "type", false)
		if len(typeIssues) != 0 {
			issues = append(issues, prefixIssues(typeIssues, "content", fmt.Sprint(index))...)
			continue
		}
		switch typeName {
		case "text":
			textValue, textIssues := requiredStringField(fields, "text", false)
			issues = append(issues, prefixIssues(textIssues, "content", fmt.Sprint(index))...)
			if len(textIssues) == 0 {
				parts = append(parts, PromptTextPart{Text: textValue})
			}
		case "image":
			mediaType, mediaIssues := requiredStringField(fields, "mediaType", false)
			data, dataIssues := requiredStringField(fields, "data", false)
			name, nameIssues := optionalStringField(fields, "name", false)
			issues = append(issues, prefixIssues(mediaIssues, "content", fmt.Sprint(index))...)
			issues = append(issues, prefixIssues(dataIssues, "content", fmt.Sprint(index))...)
			issues = append(issues, prefixIssues(nameIssues, "content", fmt.Sprint(index))...)
			if !validImageMediaType(mediaType) {
				issues = append(issues, invalidValueIssue(
					[]string{"content", fmt.Sprint(index), "mediaType"}, "Invalid image media type",
				))
			}
			if len(mediaIssues)+len(dataIssues)+len(nameIssues) == 0 && validImageMediaType(mediaType) {
				parts = append(parts, PromptImagePart{MediaType: mediaType, Data: data, Name: name})
			}
		default:
			issues = append(issues, invalidValueIssue(
				[]string{"content", fmt.Sprint(index), "type"}, "Invalid discriminator value",
			))
		}
	}
	return parts, issues
}

func decodeQueueAction(rawValue json.RawMessage) (QueueAction, []connection.ValidationIssue) {
	if len(rawValue) == 0 || bytes.Equal(bytes.TrimSpace(rawValue), []byte("null")) {
		return nil, []connection.ValidationIssue{invalidTypeIssue([]string{"action"}, "object")}
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(rawValue, &fields) != nil || fields == nil {
		return nil, []connection.ValidationIssue{invalidTypeIssue([]string{"action"}, "object")}
	}
	kind, issues := requiredStringField(fields, "kind", false)
	if len(issues) != 0 {
		return nil, prefixIssues(issues, "action")
	}
	switch kind {
	case "remove":
		return RemoveQueueAction{}, nil
	case "steer":
		return SteerQueueAction{}, nil
	case "edit":
		var content []json.RawMessage
		encoded, exists := fields["content"]
		if !exists || bytes.Equal(bytes.TrimSpace(encoded), []byte("null")) || json.Unmarshal(encoded, &content) != nil {
			return nil, []connection.ValidationIssue{invalidTypeIssue([]string{"action", "content"}, "array")}
		}
		for index, block := range content {
			var blockFields map[string]json.RawMessage
			if json.Unmarshal(block, &blockFields) != nil || blockFields == nil {
				return nil, []connection.ValidationIssue{invalidTypeIssue(
					[]string{"action", "content", fmt.Sprint(index)}, "object",
				)}
			}
			if _, fieldIssues := requiredStringField(blockFields, "type", false); len(fieldIssues) != 0 {
				return nil, prefixIssues(fieldIssues, "action", "content", fmt.Sprint(index))
			}
			content[index] = append(json.RawMessage(nil), block...)
		}
		if content == nil {
			content = []json.RawMessage{}
		}
		return EditQueueAction{Content: content}, nil
	default:
		return nil, []connection.ValidationIssue{invalidValueIssue([]string{"action", "kind"}, "Invalid discriminator value")}
	}
}

func validImageMediaType(mediaType string) bool {
	switch mediaType {
	case "image/png", "image/jpeg", "image/webp", "image/gif":
		return true
	default:
		return false
	}
}

func invalidTypeIssue(path []string, expected string) connection.ValidationIssue {
	return connection.ValidationIssue{
		Code: "invalid_type", Expected: expected, Path: path,
		Message: "Invalid input: expected " + expected,
	}
}

func invalidValueIssue(path []string, message string) connection.ValidationIssue {
	return connection.ValidationIssue{Code: "invalid_value", Path: path, Message: message}
}

func prefixIssues(issues []connection.ValidationIssue, prefix ...string) []connection.ValidationIssue {
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
