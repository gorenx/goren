package sessionapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf16"

	rpc "github.com/gorenx/goren/apiproxy/rpc"
	sessionframes "github.com/gorenx/goren/apiproxy/session/events"
	workspaceapi "github.com/gorenx/goren/apiproxy/workspace"

	"github.com/gorenx/goren/connection"
)

const maxJavaScriptSafeInteger = int64(1<<53 - 1)

// DecodeListRequest validates the source session.list payload schema.
func DecodeListRequest(rawPayload json.RawMessage) (ListRequest, []connection.ValidationIssue) {
	fields, issues := rpc.ObjectFields(rawPayload)
	if len(issues) != 0 {
		return ListRequest{}, issues
	}
	cursor, cursorIssues := rpc.OptionalString(fields, "cursor", true)
	limit, limitIssues := optionalSafeIntegerField(fields, "limit", true)
	issues = append(issues, cursorIssues...)
	issues = append(issues, limitIssues...)
	return ListRequest{
		Cursor: cursor,
		Limit:  limit,
	}, issues
}

// DecodeSearchRequest validates the source trimmed, non-empty,
// 500-JavaScript-character query contract.
func DecodeSearchRequest(rawPayload json.RawMessage) (SearchRequest, []connection.ValidationIssue) {
	fields, issues := rpc.ObjectFields(rawPayload)
	if len(issues) != 0 {
		return SearchRequest{}, issues
	}
	rawQuery, fieldIssues := rpc.RequiredString(fields, "query", false)
	if len(fieldIssues) != 0 {
		return SearchRequest{}, fieldIssues
	}
	queryText := strings.TrimSpace(rawQuery)
	if queryText == "" {
		return SearchRequest{}, []connection.ValidationIssue{{
			Code:    "too_small",
			Path:    []string{"query"},
			Message: "Too small: expected string to have >=1 characters",
		}}
	}
	if javaScriptStringLength(queryText) > 500 {
		return SearchRequest{}, []connection.ValidationIssue{{
			Code:    "too_big",
			Path:    []string{"query"},
			Message: "Too big: expected string to have <=500 characters",
		}}
	}
	if strings.ContainsRune(queryText, '\x00') {
		return SearchRequest{}, []connection.ValidationIssue{{
			Code:    "custom",
			Path:    []string{"query"},
			Message: "search query must not contain NUL",
		}}
	}
	return SearchRequest{
		Query: queryText,
	}, nil
}

func javaScriptStringLength(textValue string) int {
	count := 0
	for _, character := range textValue {
		count += utf16.RuneLen(character)
	}
	return count
}

// DecodeCreateRequest validates the source session.create payload schema.
func DecodeCreateRequest(rawPayload json.RawMessage) (CreateRequest, []connection.ValidationIssue) {
	fields, issues := rpc.ObjectFields(rawPayload)
	if len(issues) != 0 {
		return CreateRequest{}, issues
	}
	workspaceValue, workspaceIssues := rpc.OptionalString(fields, "workspaceId", true)
	cwd, cwdIssues := rpc.OptionalString(fields, "cwd", false)
	identifierValue, identifierIssues := rpc.OptionalString(fields, "sessionId", true)
	preset, presetIssues := rpc.OptionalString(fields, "agentPreset", false)
	issues = append(issues, workspaceIssues...)
	issues = append(issues, cwdIssues...)
	issues = append(issues, identifierIssues...)
	issues = append(issues, presetIssues...)
	if workspaceValue != nil && cwd != nil {
		issues = append(issues, connection.ValidationIssue{
			Code:    "custom",
			Path:    []string{},
			Message: "session.create accepts workspaceId or cwd, not both",
		})
	}
	if len(issues) != 0 {
		return CreateRequest{}, issues
	}
	var workspaceKey *workspaceapi.ID
	if workspaceValue != nil {
		converted := workspaceapi.ID(*workspaceValue)
		workspaceKey = &converted
	}
	var identifier *sessionframes.SessionID
	if identifierValue != nil {
		converted := sessionframes.SessionID(*identifierValue)
		identifier = &converted
	}
	return CreateRequest{
		WorkspaceID: workspaceKey,
		CWD:         cwd,
		SessionID:   identifier,
		AgentPreset: preset,
	}, nil
}

// DecodeRenameRequest validates the source session.rename payload schema.
func DecodeRenameRequest(rawPayload json.RawMessage) (RenameRequest, []connection.ValidationIssue) {
	fields, issues := rpc.ObjectFields(rawPayload)
	if len(issues) != 0 {
		return RenameRequest{}, issues
	}
	identifier, identifierIssues := rpc.RequiredString(fields, "sessionId", true)
	title, titleIssues := rpc.RequiredString(fields, "title", false)
	issues = append(issues, identifierIssues...)
	issues = append(issues, titleIssues...)
	return RenameRequest{
		SessionID: sessionframes.SessionID(identifier),
		Title:     title,
	}, issues
}

// DecodeHistoryRequest validates the source session.history payload schema.
func DecodeHistoryRequest(rawPayload json.RawMessage) (HistoryRequest, []connection.ValidationIssue) {
	fields, issues := rpc.ObjectFields(rawPayload)
	if len(issues) != 0 {
		return HistoryRequest{}, issues
	}
	identifier, identifierIssues := rpc.RequiredString(fields, "sessionId", true)
	beforeSeq, beforeIssues := optionalSafeIntegerField(fields, "beforeSeq", false)
	maxMessages, maxIssues := optionalSafeIntegerField(fields, "maxMessages", true)
	issues = append(issues, identifierIssues...)
	issues = append(issues, beforeIssues...)
	issues = append(issues, maxIssues...)
	return HistoryRequest{
		SessionID:   sessionframes.SessionID(identifier),
		BeforeSeq:   beforeSeq,
		MaxMessages: maxMessages,
	}, issues
}

// DecodeModelsRequest validates the source session.models payload schema.
func DecodeModelsRequest(rawPayload json.RawMessage) (ModelsRequest, []connection.ValidationIssue) {
	fields, issues := rpc.ObjectFields(rawPayload)
	if len(issues) != 0 {
		return ModelsRequest{}, issues
	}
	identifier, fieldIssues := rpc.RequiredString(fields, "sessionId", true)
	return ModelsRequest{
		SessionID: sessionframes.SessionID(identifier),
	}, fieldIssues
}

// DecodeSelectModelRequest validates the source session.selectModel payload schema.
func DecodeSelectModelRequest(rawPayload json.RawMessage) (SelectModelRequest, []connection.ValidationIssue) {
	fields, issues := rpc.ObjectFields(rawPayload)
	if len(issues) != 0 {
		return SelectModelRequest{}, issues
	}
	identifier, identifierIssues := rpc.RequiredString(fields, "sessionId", true)
	provider, providerIssues := rpc.RequiredString(fields, "provider", true)
	modelID, modelIssues := rpc.RequiredString(fields, "model", true)
	effort, effortIssues := rpc.OptionalString(fields, "reasoningEffort", true)
	issues = append(issues, identifierIssues...)
	issues = append(issues, providerIssues...)
	issues = append(issues, modelIssues...)
	issues = append(issues, effortIssues...)
	return SelectModelRequest{
		SessionID:       sessionframes.SessionID(identifier),
		Provider:        provider,
		Model:           modelID,
		ReasoningEffort: effort,
	}, issues
}

// DecodePromptRequest validates the source session.prompt payload schema.
func DecodePromptRequest(rawPayload json.RawMessage) (PromptRequest, []connection.ValidationIssue) {
	fields, issues := rpc.ObjectFields(rawPayload)
	if len(issues) != 0 {
		return PromptRequest{}, issues
	}
	identifier, identifierIssues := rpc.RequiredString(fields, "sessionId", true)
	mode, modeIssues := rpc.RequiredString(fields, "mode", false)
	zone, zoneIssues := rpc.OptionalString(fields, "clientTimeZone", false)
	issues = append(issues, identifierIssues...)
	issues = append(issues, modeIssues...)
	issues = append(issues, zoneIssues...)
	if mode != "queue" && mode != "steer" {
		issues = append(issues, invalidValueIssue([]string{"mode"}, "Invalid option: expected one of \"queue\"|\"steer\""))
	}
	parts, contentIssues := decodePromptParts(fields["content"])
	issues = append(issues, contentIssues...)
	return PromptRequest{
		SessionID:      sessionframes.SessionID(identifier),
		Mode:           mode,
		Content:        parts,
		ClientTimeZone: zone,
	}, issues
}

// DecodeUpdateQueueRequest validates the source session.updateQueue payload schema.
func DecodeUpdateQueueRequest(rawPayload json.RawMessage) (UpdateQueueRequest, []connection.ValidationIssue) {
	fields, issues := rpc.ObjectFields(rawPayload)
	if len(issues) != 0 {
		return UpdateQueueRequest{}, issues
	}
	identifier, identifierIssues := rpc.RequiredString(fields, "sessionId", true)
	itemID, itemIssues := rpc.RequiredString(fields, "itemId", true)
	action, actionIssues := decodeQueueAction(fields["action"])
	issues = append(issues, identifierIssues...)
	issues = append(issues, itemIssues...)
	issues = append(issues, actionIssues...)
	return UpdateQueueRequest{
		SessionID: sessionframes.SessionID(identifier),
		ItemID:    sessionframes.MessageID(itemID),
		Action:    action,
	}, issues
}

// DecodeCancelRequest validates the source session.cancel payload schema.
func DecodeCancelRequest(rawPayload json.RawMessage) (CancelRequest, []connection.ValidationIssue) {
	fields, issues := rpc.ObjectFields(rawPayload)
	if len(issues) != 0 {
		return CancelRequest{}, issues
	}
	identifier, fieldIssues := rpc.RequiredString(fields, "sessionId", true)
	return CancelRequest{
		SessionID: sessionframes.SessionID(identifier),
	}, fieldIssues
}

func optionalSafeIntegerField(fields map[string]json.RawMessage, name string, positive bool) (*int64, []connection.ValidationIssue) {
	rawValue, exists := fields[name]
	if !exists {
		return nil, nil
	}
	if bytes.Equal(bytes.TrimSpace(rawValue), []byte("null")) {
		return nil, []connection.ValidationIssue{rpc.InvalidType([]string{name}, "number")}
	}
	var numberValue json.Number
	decoder := json.NewDecoder(bytes.NewReader(rawValue))
	decoder.UseNumber()
	if err := decoder.Decode(&numberValue); err != nil {
		return nil, []connection.ValidationIssue{rpc.InvalidType([]string{name}, "number")}
	}
	integerValue, err := numberValue.Int64()
	if err != nil || integerValue < 0 || integerValue > maxJavaScriptSafeInteger || positive && integerValue == 0 {
		return nil, []connection.ValidationIssue{{
			Code:     "invalid_type",
			Expected: "integer",
			Path:     []string{name},
			Message:  "Invalid input: expected integer",
		}}
	}
	return &integerValue, nil
}

func decodePromptParts(rawValue json.RawMessage) ([]PromptContentPart, []connection.ValidationIssue) {
	if len(rawValue) == 0 || bytes.Equal(bytes.TrimSpace(rawValue), []byte("null")) {
		return nil, []connection.ValidationIssue{rpc.InvalidType([]string{"content"}, "array")}
	}
	var encoded []json.RawMessage
	if err := json.Unmarshal(rawValue, &encoded); err != nil {
		return nil, []connection.ValidationIssue{rpc.InvalidType([]string{"content"}, "array")}
	}
	parts := make([]PromptContentPart, 0, len(encoded))
	issues := make([]connection.ValidationIssue, 0)
	for index, encodedPart := range encoded {
		var fields map[string]json.RawMessage
		if !json.Valid(encodedPart) || json.Unmarshal(encodedPart, &fields) != nil || fields == nil {
			issues = append(issues, rpc.InvalidType([]string{"content", fmt.Sprint(index)}, "object"))
			continue
		}
		typeName, typeIssues := rpc.RequiredString(fields, "type", false)
		if len(typeIssues) != 0 {
			issues = append(issues, rpc.PrefixIssues(typeIssues, "content", fmt.Sprint(index))...)
			continue
		}
		switch typeName {
		case "text":
			textValue, textIssues := rpc.RequiredString(fields, "text", false)
			issues = append(issues, rpc.PrefixIssues(textIssues, "content", fmt.Sprint(index))...)
			if len(textIssues) == 0 {
				parts = append(parts, PromptTextPart{
					Text: textValue,
				})
			}
		case "image":
			mediaType, mediaIssues := rpc.RequiredString(fields, "mediaType", false)
			data, dataIssues := rpc.RequiredString(fields, "data", false)
			name, nameIssues := rpc.OptionalString(fields, "name", false)
			issues = append(issues, rpc.PrefixIssues(mediaIssues, "content", fmt.Sprint(index))...)
			issues = append(issues, rpc.PrefixIssues(dataIssues, "content", fmt.Sprint(index))...)
			issues = append(issues, rpc.PrefixIssues(nameIssues, "content", fmt.Sprint(index))...)
			if !validImageMediaType(mediaType) {
				issues = append(issues, invalidValueIssue(
					[]string{"content", fmt.Sprint(index), "mediaType"}, "Invalid image media type",
				))
			}
			if len(mediaIssues)+len(dataIssues)+len(nameIssues) == 0 && validImageMediaType(mediaType) {
				parts = append(parts, PromptImagePart{
					MediaType: mediaType,
					Data:      data,
					Name:      name,
				})
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
		return nil, []connection.ValidationIssue{rpc.InvalidType([]string{"action"}, "object")}
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(rawValue, &fields) != nil || fields == nil {
		return nil, []connection.ValidationIssue{rpc.InvalidType([]string{"action"}, "object")}
	}
	kind, issues := rpc.RequiredString(fields, "kind", false)
	if len(issues) != 0 {
		return nil, rpc.PrefixIssues(issues, "action")
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
			return nil, []connection.ValidationIssue{rpc.InvalidType([]string{"action", "content"}, "array")}
		}
		for index, block := range content {
			var blockFields map[string]json.RawMessage
			if json.Unmarshal(block, &blockFields) != nil || blockFields == nil {
				return nil, []connection.ValidationIssue{rpc.InvalidType(
					[]string{"action", "content", fmt.Sprint(index)}, "object",
				)}
			}
			if _, fieldIssues := rpc.RequiredString(blockFields, "type", false); len(fieldIssues) != 0 {
				return nil, rpc.PrefixIssues(fieldIssues, "action", "content", fmt.Sprint(index))
			}
			content[index] = append(json.RawMessage(nil), block...)
		}
		if content == nil {
			content = []json.RawMessage{}
		}
		return EditQueueAction{
			Content: content,
		}, nil
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

func invalidValueIssue(path []string, message string) connection.ValidationIssue {
	return connection.ValidationIssue{
		Code:    "invalid_value",
		Path:    path,
		Message: message,
	}
}
