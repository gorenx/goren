package llm_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gorenx/goren/llm"
)

func TestPrepareContextTransformsCrossModelHistoryWithoutMutation(t *testing.T) {
	timestamp := time.Date(2026, 8, 13, 1, 2, 3, 0, time.UTC)
	sourceMetadata := &llm.ReplayMetadata{
		API: "source-api", Provider: "source-provider", Model: "source-model",
		Data: json.RawMessage(`{"item_id":"source-item"}`),
	}
	originalCallID := "call|with spaces|and incompatible punctuation"
	original := llm.Context{Messages: []llm.Message{
		llm.AssistantMessage{
			API: "source-api", Provider: "source-provider", Model: "source-model",
			StopReason: llm.StopReasonToolUse, Timestamp: timestamp,
			Content: []llm.AssistantContent{
				llm.ThinkingContent{Thinking: "consider evidence", Signature: "encrypted", Metadata: sourceMetadata},
				llm.AssistantTextContent{Text: "working", Phase: llm.AssistantTextPhaseCommentary, Metadata: sourceMetadata},
				llm.ToolCall{ID: originalCallID, Name: "lookup", Arguments: json.RawMessage(`{"q":"memory"}`), Metadata: sourceMetadata},
			},
		},
		llm.ToolResultMessage{ToolCallID: originalCallID, ToolName: "lookup", Content: []llm.ToolResultContent{llm.TextContent{Text: "found"}}, Timestamp: timestamp},
		llm.AssistantMessage{API: "source-api", Provider: "source-provider", Model: "source-model", StopReason: llm.StopReasonError, ErrorMessage: "disconnect", Content: []llm.AssistantContent{llm.AssistantTextContent{Text: "partial"}}},
	}}

	target := validModel("target-api", "target-provider")
	prepared, err := llm.PrepareContext(target, original)
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.Messages) != 2 {
		t.Fatalf("got %d prepared messages: %#v", len(prepared.Messages), prepared.Messages)
	}
	assistantReply := prepared.Messages[0].(llm.AssistantMessage)
	if len(assistantReply.Content) != 3 {
		t.Fatalf("got content %#v", assistantReply.Content)
	}
	convertedThinking, ok := assistantReply.Content[0].(llm.AssistantTextContent)
	if !ok || convertedThinking.Text != "consider evidence" || convertedThinking.Metadata != nil {
		t.Fatalf("thinking was not downgraded safely: %#v", assistantReply.Content[0])
	}
	visible := assistantReply.Content[1].(llm.AssistantTextContent)
	if visible.Phase != llm.AssistantTextPhaseCommentary || visible.Metadata != nil {
		t.Fatalf("cross-model text metadata was retained: %#v", visible)
	}
	preparedCall := assistantReply.Content[2].(llm.ToolCall)
	if preparedCall.ID == originalCallID || preparedCall.Metadata != nil || len(preparedCall.ID) > 64 {
		t.Fatalf("tool call was not normalized: %#v", preparedCall)
	}
	preparedResult := prepared.Messages[1].(llm.ToolResultMessage)
	if preparedResult.ToolCallID != preparedCall.ID {
		t.Fatalf("result ID %q does not match call ID %q", preparedResult.ToolCallID, preparedCall.ID)
	}
	originalCall := original.Messages[0].(llm.AssistantMessage).Content[2].(llm.ToolCall)
	if originalCall.ID != originalCallID || originalCall.Metadata == nil {
		t.Fatal("PrepareContext modified the caller-owned Context")
	}
}

func TestPrepareContextRepairsOrphansAndRejectsUnsupportedImages(t *testing.T) {
	target := validModel("api", "provider")
	orphan := llm.AssistantMessage{
		API: target.API, Provider: target.Provider, Model: target.ID,
		StopReason: llm.StopReasonToolUse,
		Content:    []llm.AssistantContent{llm.ToolCall{ID: "call-1", Name: "lookup", Arguments: json.RawMessage(`{}`)}},
	}
	prepared, err := llm.PrepareContext(target, llm.Context{Messages: []llm.Message{orphan, llm.NewTextMessage("continue")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.Messages) != 3 {
		t.Fatalf("got messages %#v", prepared.Messages)
	}
	synthetic := prepared.Messages[1].(llm.ToolResultMessage)
	if !synthetic.IsError || synthetic.ToolCallID != "call-1" {
		t.Fatalf("got synthetic result %#v", synthetic)
	}

	_, err = llm.PrepareContext(target, llm.Context{Messages: []llm.Message{llm.UserMessage{
		Content: []llm.UserContent{llm.ImageContent{Data: "aGVsbG8=", MIMEType: "image/png"}},
	}}})
	if !errors.Is(err, llm.ErrUnsupportedModality) {
		t.Fatalf("got image error %v", err)
	}
}

func TestContextJSONRoundTripPreservesTaggedContentAndMetadata(t *testing.T) {
	timestamp := time.Date(2026, 8, 13, 4, 5, 6, 7, time.UTC)
	metadata := &llm.ReplayMetadata{API: llm.APIOpenAIResponses, Provider: "openai", Model: "gpt", Data: json.RawMessage(`{"item_id":"msg_1"}`)}
	original := llm.Context{
		SystemPrompt: "system",
		Messages: []llm.Message{
			llm.UserMessage{Timestamp: timestamp, Content: []llm.UserContent{llm.TextContent{Text: "hello"}, llm.ImageContent{Data: "aA==", MIMEType: "image/png"}}},
			llm.AssistantMessage{API: llm.APIOpenAIResponses, Provider: "openai", Model: "gpt", StopReason: llm.StopReasonToolUse, Timestamp: timestamp, Content: []llm.AssistantContent{
				llm.AssistantTextContent{Text: "working", Phase: llm.AssistantTextPhaseCommentary, Metadata: metadata},
				llm.ThinkingContent{Thinking: "reason", Signature: "sig", Metadata: metadata},
				llm.ToolCall{ID: "call_1", Name: "lookup", Arguments: json.RawMessage(`{"q":1}`), Metadata: metadata},
			}},
			llm.ToolResultMessage{ToolCallID: "call_1", ToolName: "lookup", IsError: true, Timestamp: timestamp, Content: []llm.ToolResultContent{llm.TextContent{Text: "failed"}}},
		},
		Tools: []llm.Tool{{Name: "lookup", Parameters: json.RawMessage(`{"type":"object"}`), Strict: true}},
	}
	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var restored llm.Context
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(original, restored) {
		t.Fatalf("round trip mismatch\noriginal: %#v\nrestored: %#v", original, restored)
	}

	unknownVersion := strings.Replace(string(encoded), `"version":1`, `"version":99`, 1)
	if err := json.Unmarshal([]byte(unknownVersion), &restored); err == nil {
		t.Fatal("expected unknown version to fail")
	}
	unknownContent := strings.Replace(string(encoded), `"type":"text"`, `"type":"audio"`, 1)
	if err := json.Unmarshal([]byte(unknownContent), &restored); err == nil {
		t.Fatal("expected unknown content type to fail")
	}
}

func TestValidateToolCallUsesStrictJSONSchema(t *testing.T) {
	tools := []llm.Tool{{
		Name: "lookup",
		Parameters: json.RawMessage(`{
			"type":"object",
			"properties":{"limit":{"type":"integer"}},
			"required":["limit"],
			"additionalProperties":false
		}`),
	}}
	requestedCall := llm.ToolCall{Name: "lookup", Arguments: json.RawMessage(`{"limit":"3"}`)}
	err := llm.ValidateToolCall(tools, requestedCall)
	if err == nil || !strings.Contains(err.Error(), "lookup") || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("got validation error %v", err)
	}
	if string(requestedCall.Arguments) != `{"limit":"3"}` {
		t.Fatal("validation modified tool arguments")
	}
}

func TestResolveReasoningClampsAndMapsCapabilities(t *testing.T) {
	target := validModel("api", "provider")
	target.Reasoning = true
	target.ReasoningLevels = []llm.ReasoningLevel{llm.ReasoningLow, llm.ReasoningHigh}
	target.ReasoningMap = map[llm.ReasoningLevel]string{llm.ReasoningHigh: "provider-high"}
	target.ReasoningBudget = map[llm.ReasoningLevel]int{llm.ReasoningHigh: 4096}
	resolved, mapped, budget, err := llm.ResolveReasoning(target, llm.ReasoningXHigh)
	if err != nil || resolved != llm.ReasoningHigh || mapped != "provider-high" || budget != 4096 {
		t.Fatalf("resolved=%q mapped=%q budget=%d err=%v", resolved, mapped, budget, err)
	}
}

func TestResolveStreamOptionsAppliesModelReasoningBudget(t *testing.T) {
	target := validModel("api", "provider")
	target.Reasoning = true
	target.ReasoningLevels = []llm.ReasoningLevel{llm.ReasoningLow, llm.ReasoningHigh}
	target.ReasoningBudget = map[llm.ReasoningLevel]int{llm.ReasoningHigh: 4096}
	resolved, err := llm.ResolveStreamOptions(target, llm.StreamOptions{Reasoning: llm.ReasoningXHigh})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Reasoning != llm.ReasoningHigh || resolved.ThinkingBudget != 4096 {
		t.Fatalf("got resolved options %#v", resolved)
	}
}

func TestValidateOptionsRejectsInvalidRequestID(t *testing.T) {
	if err := llm.ValidateOptions(llm.StreamOptions{RequestID: strings.Repeat("a", 513)}); err == nil {
		t.Fatal("expected an overlong request ID to fail")
	}
	if err := llm.ValidateOptions(llm.StreamOptions{RequestID: "请求-1"}); err == nil {
		t.Fatal("expected a non-ASCII request ID to fail")
	}
	if err := llm.ValidateOptions(llm.StreamOptions{RequestID: "request\n1"}); err == nil {
		t.Fatal("expected a control character in request ID to fail")
	}
	if err := llm.ValidateOptions(llm.StreamOptions{RequestID: strings.Repeat("a", 512)}); err != nil {
		t.Fatalf("valid request ID failed: %v", err)
	}
}

func TestStreamOptionsCloneIsolatesAsynchronousInputs(t *testing.T) {
	temperature := 0.5
	retries := 2
	original := llm.StreamOptions{
		Temperature: &temperature,
		MaxRetries:  &retries,
		Headers:     map[string]string{"X-Test": "original"},
		Metadata:    map[string]string{"trace": "original"},
		ResponseFormat: &llm.JSONSchemaFormat{
			Name:   "answer",
			Schema: json.RawMessage(`{"type":"object"}`),
		},
		ToolChoice: &llm.ToolChoice{Mode: llm.ToolChoiceFunction, Name: "lookup"},
	}
	snapshot := original.Clone()
	temperature = 1.5
	retries = 9
	original.Headers["X-Test"] = "mutated"
	original.Metadata["trace"] = "mutated"
	original.ResponseFormat.Schema[0] = '['
	original.ToolChoice.Name = "mutated"

	if *snapshot.Temperature != 0.5 || *snapshot.MaxRetries != 2 ||
		snapshot.Headers["X-Test"] != "original" || snapshot.Metadata["trace"] != "original" ||
		string(snapshot.ResponseFormat.Schema) != `{"type":"object"}` || snapshot.ToolChoice.Name != "lookup" {
		t.Fatalf("clone shares caller-owned state: %#v", snapshot)
	}
}

func TestEventStreamFailurePreservesPartialSnapshot(t *testing.T) {
	target := validModel("api", "provider")
	responseStream := llm.NewEventStream(context.Background(), target, func(_ context.Context, eventSink llm.StreamEmitter) {
		partial := llm.AssistantMessage{
			API: target.API, Provider: target.Provider, Model: target.ID,
			ResponseID: "response-1", ResponseModel: "served-model",
			Usage:   llm.Usage{InputTokens: 3},
			Content: []llm.AssistantContent{llm.AssistantTextContent{Text: "par"}},
		}
		eventSink.Update(partial)
		eventSink.Emit(llm.TextDeltaEvent{ContentIndex: 0, Delta: "par"})
		eventSink.FailWith(partial, errors.New("disconnect"))
	})

	streamedEvent, ok, err := responseStream.Next(context.Background())
	if err != nil || !ok {
		t.Fatalf("read delta: ok=%v err=%v", ok, err)
	}
	if _, ok := streamedEvent.(llm.TextDeltaEvent); !ok {
		t.Fatalf("got event %T", streamedEvent)
	}
	if snapshot := responseStream.Snapshot(); llm.Text(snapshot) != "par" || snapshot.ResponseID != "response-1" {
		t.Fatalf("got snapshot %#v", snapshot)
	}
	assistantReply, err := responseStream.Result(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if assistantReply.StopReason != llm.StopReasonError || llm.Text(assistantReply) != "par" || assistantReply.Usage.InputTokens != 3 {
		t.Fatalf("got terminal partial %#v", assistantReply)
	}
}
