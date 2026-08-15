package apiproxy

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/userquestions"
)

func decodeQuestionResponse(
	result connection.RPCResult,
	expectedSessionID SessionID,
	requestedItems []AskUserQuestionItem,
) (questionClientResolution, bool) {
	if !result.OK {
		if result.Error != nil && result.Error.Code == connection.ErrorCancelled {
			return questionClientResolution{cancelled: true}, true
		}
		return questionClientResolution{}, false
	}
	if result.Error != nil {
		return questionClientResolution{}, false
	}
	answerValue, conversationID, valid := decodeQuestionPayload(result.Value)
	if !valid || conversationID != expectedSessionID || !matchesQuestionItems(answerValue, requestedItems) {
		return questionClientResolution{}, false
	}
	return questionClientResolution{answerValue: answerValue}, true
}

func decodeQuestionPayload(rawValue json.RawMessage) (userquestions.Answer, SessionID, bool) {
	var envelope struct {
		SessionID *SessionID      `json:"sessionId"`
		Answer    json.RawMessage `json:"answer"`
	}
	if err := json.Unmarshal(rawValue, &envelope); err != nil || envelope.SessionID == nil ||
		!isObjectJSON(envelope.Answer) {
		return userquestions.Answer{}, "", false
	}
	var answerEnvelope struct {
		Answers json.RawMessage `json:"answers"`
	}
	if err := json.Unmarshal(envelope.Answer, &answerEnvelope); err != nil || !isArrayJSON(answerEnvelope.Answers) {
		return userquestions.Answer{}, "", false
	}
	var rawItems []json.RawMessage
	if err := json.Unmarshal(answerEnvelope.Answers, &rawItems); err != nil {
		return userquestions.Answer{}, "", false
	}
	answerItems := make([]userquestions.AnswerItem, 0, len(rawItems))
	for _, rawItem := range rawItems {
		decoded, valid := decodeQuestionAnswerItem(rawItem)
		if !valid {
			return userquestions.Answer{}, "", false
		}
		answerItems = append(answerItems, decoded)
	}
	return userquestions.Answer{Answers: answerItems}, *envelope.SessionID, true
}

func decodeQuestionAnswerItem(rawValue json.RawMessage) (userquestions.AnswerItem, bool) {
	if !isObjectJSON(rawValue) {
		return userquestions.AnswerItem{}, false
	}
	var fields struct {
		ID       *string         `json:"id"`
		Selected json.RawMessage `json:"selected"`
		Custom   json.RawMessage `json:"custom"`
	}
	if err := json.Unmarshal(rawValue, &fields); err != nil || fields.ID == nil || !isArrayJSON(fields.Selected) {
		return userquestions.AnswerItem{}, false
	}
	var selectedValues []string
	if err := json.Unmarshal(fields.Selected, &selectedValues); err != nil {
		return userquestions.AnswerItem{}, false
	}
	decoded := userquestions.AnswerItem{ID: *fields.ID, Selected: selectedValues}
	if len(fields.Custom) != 0 {
		if bytes.Equal(bytes.TrimSpace(fields.Custom), []byte("null")) {
			return userquestions.AnswerItem{}, false
		}
		var customText string
		if err := json.Unmarshal(fields.Custom, &customText); err != nil {
			return userquestions.AnswerItem{}, false
		}
		decoded.Custom = &customText
	}
	return decoded, true
}

func matchesQuestionItems(answerValue userquestions.Answer, requestedItems []AskUserQuestionItem) bool {
	if len(answerValue.Answers) != len(requestedItems) {
		return false
	}
	for index, answerItem := range answerValue.Answers {
		questionItem := requestedItems[index]
		if answerItem.ID != questionItem.ID || duplicateSelection(answerItem.Selected) {
			return false
		}
		if answerItem.Custom != nil && strings.TrimSpace(*answerItem.Custom) == "" {
			return false
		}
		if questionItem.MultiSelect == nil || !*questionItem.MultiSelect {
			if answerItem.Custom != nil && len(answerItem.Selected) != 0 {
				return false
			}
			if len(answerItem.Selected) > 1 {
				return false
			}
		}
		offered := make(map[string]struct{})
		if questionItem.Options != nil {
			for _, optionValue := range *questionItem.Options {
				offered[optionValue.Label] = struct{}{}
			}
		}
		for _, selectedLabel := range answerItem.Selected {
			if _, exists := offered[selectedLabel]; !exists {
				return false
			}
		}
	}
	return true
}

func duplicateSelection(selectedValues []string) bool {
	seen := make(map[string]struct{}, len(selectedValues))
	for _, selectedLabel := range selectedValues {
		if _, exists := seen[selectedLabel]; exists {
			return true
		}
		seen[selectedLabel] = struct{}{}
	}
	return false
}

func isObjectJSON(rawValue json.RawMessage) bool {
	trimmed := bytes.TrimSpace(rawValue)
	return len(trimmed) >= 2 && trimmed[0] == '{' && trimmed[len(trimmed)-1] == '}'
}

func isArrayJSON(rawValue json.RawMessage) bool {
	trimmed := bytes.TrimSpace(rawValue)
	return len(trimmed) >= 2 && trimmed[0] == '[' && trimmed[len(trimmed)-1] == ']'
}
