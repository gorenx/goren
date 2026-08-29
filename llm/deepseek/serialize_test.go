package deepseek

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/attachment"
	"github.com/gorenx/goren/llm"
)

func TestSerializeMessagesPreservesDeepSeekReplayRules(t *testing.T) {
	t.Parallel()
	assistant := mustMessage(t, agentmessage.RoleAssistant, []agentmessage.ContentBlock{
		agentmessage.ReasoningBlock{
			Type: "reasoning",
			Text: "think",
		},
		agentmessage.ToolCallBlock{
			Type:      "tool-call",
			ID:        "call-1",
			Name:      "lookup",
			Arguments: `{"q":"x"}`,
		},
	})
	user := mustMessage(t, agentmessage.RoleUser, []agentmessage.ContentBlock{
		agentmessage.TextBlock{
			Type: "text",
			Text: "note",
		},
		agentmessage.ToolResultBlock{
			Type:       "tool-result",
			ToolCallID: "call-1",
			Content:    nil,
		},
	})
	wireMessages, err := SerializeMessages([]agentmessage.Message{assistant, user})
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, wireMessages, []any{
		map[string]any{
			"role": "assistant", "content": "", "reasoning_content": "think",
			"tool_calls": []any{map[string]any{
				"id": "call-1", "type": "function",
				"function": map[string]any{"name": "lookup", "arguments": `{"q":"x"}`},
			}},
		},
		map[string]any{"role": "user", "content": "note"},
		map[string]any{"role": "tool", "tool_call_id": "call-1", "content": emptyToolOutput},
	})
}

func TestSerializeMessagesRejectsNestedImage(t *testing.T) {
	t.Parallel()
	entry := mustMessage(t, agentmessage.RoleUser, []agentmessage.ContentBlock{agentmessage.ToolResultBlock{
		Type:       "tool-result",
		ToolCallID: "call-1",
		Content: []agentmessage.ContentBlock{agentmessage.ImageBlock{
			Type:       "image",
			Attachment: attachment.ImageAttachmentRef{},
		}},
	}})
	_, err := SerializeMessages([]agentmessage.Message{entry})
	var providerFailure *llm.LlmError
	if !errors.As(err, &providerFailure) || providerFailure.Code() != "UNSUPPORTED_CONTENT" {
		t.Fatalf("error = %#v", err)
	}
}

func TestSerializeRequestThinkingAndOptionalFields(t *testing.T) {
	t.Parallel()
	high := ReasoningHigh
	disabled := ThinkingDisabled
	maximumTokens := 32
	emptyStops := []string{}
	requestOptions := llm.GenerateOptions{
		CallConfig: llm.CallConfig{
			Model:           "m",
			ReasoningEffort: llm.ReasoningEffortID(ReasoningHigh),
			MaxTokens:       &maximumTokens,
			Stop:            emptyStops,
		},
		Tools: []llm.ToolSchema{
			{
				Name:        "lookup",
				Description: "Lookup",
				Parameters:  json.RawMessage(`{"type":"object"}`),
			},
		},
	}
	wireValue, err := SerializeRequest(requestOptions, RequestDefaults{
		ReasoningEffort: &high,
	})
	if err != nil {
		t.Fatal(err)
	}
	if wireValue.Thinking == nil || wireValue.Thinking.Type != ThinkingEnabled ||
		wireValue.ReasoningEffort == nil || *wireValue.ReasoningEffort != ReasoningHigh ||
		wireValue.Stop == nil || len(*wireValue.Stop) != 0 || len(wireValue.Tools) != 1 {
		t.Fatalf("wire request = %#v", wireValue)
	}
	_, err = SerializeRequest(requestOptions, RequestDefaults{
		Thinking: &disabled,
	})
	var providerFailure *llm.LlmError
	if !errors.As(err, &providerFailure) || providerFailure.Code() != "UNSUPPORTED_REASONING_EFFORT" {
		t.Fatalf("disabled-thinking error = %#v", err)
	}
	requestOptions.Purpose = llm.PurposeSessionTitle
	wireValue, err = SerializeRequest(requestOptions, RequestDefaults{
		Thinking: &disabled,
	})
	if err != nil || wireValue.Thinking == nil || wireValue.Thinking.Type != ThinkingDisabled || wireValue.ReasoningEffort != nil {
		t.Fatalf("session-title request = (%#v, %v)", wireValue, err)
	}
}

func mustMessage(t *testing.T, role agentmessage.MessageRole, content []agentmessage.ContentBlock) agentmessage.Message {
	t.Helper()
	entry, err := agentmessage.NewMessage(agentmessage.MessageInput{
		Role:    role,
		Content: content,
		Source: agentmessage.PluginMessageSource{
			Plugin: "test",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return entry
}

func assertJSONEqual(t *testing.T, actual any, expected any) {
	t.Helper()
	encoded, err := json.Marshal(actual)
	if err != nil {
		t.Fatal(err)
	}
	var normalized any
	if err := json.Unmarshal(encoded, &normalized); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(normalized, expected) {
		t.Fatalf("JSON = %s, want %#v", encoded, expected)
	}
}
