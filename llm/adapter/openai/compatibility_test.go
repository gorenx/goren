package openai_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorenx/goren/llm"
	openaiadapter "github.com/gorenx/goren/llm/adapter/openai"
	llmfactory "github.com/gorenx/goren/llm/factory"
)

func TestChatCompatibilityAndInvocationControls(t *testing.T) {
	requestBody := make(chan map[string]any, 1)
	testServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("x-client-request-id") != "request-1" || request.Header.Get("x-session-affinity") != "session-1" {
			t.Errorf("got request headers %#v", request.Header)
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		requestBody <- body
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.Header().Set("x-request-id", "provider-request")
		writeSSE(writer, `{"id":"response-controls","model":"served-model","service_tier":"priority","choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`)
		writeSSE(writer, `{"choices":[],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}`)
		fmt.Fprint(writer, "data: [DONE]\n\n")
	}))
	defer testServer.Close()

	targetModel := model(testServer.URL)
	targetModel.Reasoning = true
	targetModel.ReasoningLevels = []llm.ReasoningLevel{llm.ReasoningLow, llm.ReasoningHigh}
	targetModel.ReasoningMap = map[llm.ReasoningLevel]string{llm.ReasoningHigh: "vendor_high"}
	targetModel.ReasoningBudget = map[llm.ReasoningLevel]int{llm.ReasoningHigh: 4096}
	targetModel.Cost = llm.CostRates{Input: 1, Output: 2}
	targetModel.ServiceTierCost = map[string]float64{"priority": 2}
	compatibleBehavior := openaiadapter.Compatibility{
		SystemRole:             openaiadapter.SystemRoleDeveloper,
		MaxTokensField:         openaiadapter.MaxTokensLegacy,
		ReasoningFormat:        openaiadapter.ReasoningFormatOpenRouter,
		DisableStreamingUsage:  true,
		DisableStrictTools:     true,
		IncludeToolResultName:  true,
		SessionAffinityHeaders: []string{"x-session-affinity"},
		ThinkingBudgetField:    "reasoning.max_tokens",
		ToolErrorPrefix:        "ERROR: ",
	}
	llmClient := newConfiguredChatClient(t, targetModel, testServer.Client(), openaiadapter.WithCompatibility(compatibleBehavior))

	beforeCalled := false
	afterCalled := false
	assistantHistory := llm.AssistantMessage{
		API: targetModel.API, Provider: targetModel.Provider, Model: targetModel.ID,
		StopReason: llm.StopReasonToolUse,
		Content:    []llm.AssistantContent{llm.ToolCall{ID: "call-1", Name: "lookup", Arguments: json.RawMessage(`{"q":"memory"}`)}},
	}
	assistantReply, err := llmClient.Complete(context.Background(), llm.Context{
		SystemPrompt: "system",
		Messages: []llm.Message{
			assistantHistory,
			llm.ToolResultMessage{ToolCallID: "call-1", ToolName: "lookup", IsError: true, Content: []llm.ToolResultContent{llm.TextContent{Text: "not found"}}},
			llm.NewTextMessage("continue"),
		},
		Tools: []llm.Tool{{Name: "lookup", Parameters: json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}`), Strict: true}},
	}, llm.StreamOptions{
		APIKey:          "secret",
		MaxOutputTokens: 512,
		Reasoning:       llm.ReasoningXHigh,
		ToolChoice:      &llm.ToolChoice{Mode: llm.ToolChoiceFunction, Name: "lookup"},
		CacheKey:        "cache-1",
		CacheRetention:  llm.CacheRetention24Hours,
		SessionID:       "session-1",
		RequestID:       "request-1",
		Metadata:        map[string]string{"trace": "safe"},
		ServiceTier:     "priority",
		BeforeRequest: func(_ context.Context, observedRequest llm.RequestInfo) error {
			beforeCalled = observedRequest.RequestID == "request-1" && observedRequest.Metadata["trace"] == "safe"
			return nil
		},
		TransformRequest: func(_ context.Context, _ llm.RequestInfo, payload llm.RequestPayload) (llm.RequestPayload, error) {
			payload.Set["extension"] = "enabled"
			return payload, nil
		},
		AfterResponse: func(_ context.Context, observedResponse llm.ResponseInfo) error {
			afterCalled = observedResponse.RequestID == "provider-request" && observedResponse.StatusCode == http.StatusOK
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !beforeCalled || !afterCalled {
		t.Fatalf("hooks were not called: before=%t after=%t", beforeCalled, afterCalled)
	}
	if assistantReply.Usage.ServiceTier != "priority" || assistantReply.Usage.Cost.Total <= 0 {
		t.Fatalf("service tier cost not applied: %#v", assistantReply.Usage)
	}

	body := <-requestBody
	if body["max_tokens"] != float64(512) || body["max_completion_tokens"] != nil {
		t.Fatalf("wrong token field: %#v", body)
	}
	if streamedOptions, present := body["stream_options"]; present && streamedOptions != nil || body["extension"] != "enabled" {
		t.Fatalf("compatibility/transform not applied: %#v", body)
	}
	reasoning := body["reasoning"].(map[string]any)
	if reasoning["effort"] != "vendor_high" || reasoning["max_tokens"] != float64(4096) {
		t.Fatalf("got reasoning %#v", reasoning)
	}
	if body["prompt_cache_key"] != "cache-1" || body["prompt_cache_retention"] != "24h" || body["service_tier"] != "priority" {
		t.Fatalf("missing invocation controls: %#v", body)
	}
	selectedTool := body["tool_choice"].(map[string]any)
	if selectedTool["type"] != "function" || selectedTool["function"].(map[string]any)["name"] != "lookup" {
		t.Fatalf("got tool choice %#v", selectedTool)
	}
	tools := body["tools"].([]any)
	if _, present := tools[0].(map[string]any)["strict"]; present {
		t.Fatalf("strict tool flag was not removed: %#v", tools[0])
	}
	messages := body["messages"].([]any)
	if messages[0].(map[string]any)["role"] != "developer" {
		t.Fatalf("got system role %#v", messages[0])
	}
	toolResult := messages[2].(map[string]any)
	if toolResult["content"] != "ERROR: not found" || toolResult["name"] != "lookup" {
		t.Fatalf("got tool error result %#v", toolResult)
	}
}

func TestChatRetryDelayCapAndTimeout(t *testing.T) {
	var attempts atomic.Int32
	retryServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) == 1 {
			writer.Header().Set("Retry-After", "10")
			http.Error(writer, "retry", http.StatusTooManyRequests)
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		writeSSE(writer, `{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`)
		fmt.Fprint(writer, "data: [DONE]\n\n")
	}))
	defer retryServer.Close()

	started := time.Now()
	llmClient := newClient(t, model(retryServer.URL), retryServer.Client())
	assistantReply, err := llmClient.Complete(context.Background(), llm.Context{Messages: []llm.Message{llm.NewTextMessage("retry")}}, llm.StreamOptions{
		APIKey: "secret", MaxRetries: intPointer(1), MaxRetryDelay: time.Millisecond,
	})
	if err != nil || llm.Text(assistantReply) != "ok" || attempts.Load() != 2 {
		t.Fatalf("reply=%#v attempts=%d err=%v", assistantReply, attempts.Load(), err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("retry delay was not capped: %s", elapsed)
	}

	timeoutServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "data: [DONE]\n\n")
	}))
	defer timeoutServer.Close()
	llmClient = newClient(t, model(timeoutServer.URL), timeoutServer.Client())
	assistantReply, err = llmClient.Complete(context.Background(), llm.Context{Messages: []llm.Message{llm.NewTextMessage("timeout")}}, llm.StreamOptions{
		APIKey: "secret", Timeout: 20 * time.Millisecond,
	})
	if err != nil || assistantReply.StopReason != llm.StopReasonAborted {
		t.Fatalf("got timeout reply=%#v err=%v", assistantReply, err)
	}
}

func TestRequestTransformRejectsBodyReplacement(t *testing.T) {
	var transportCalled atomic.Bool
	testServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		transportCalled.Store(true)
		http.Error(writer, "unexpected request", http.StatusInternalServerError)
	}))
	defer testServer.Close()

	llmClient := newClient(t, model(testServer.URL), testServer.Client())
	assistantReply, err := llmClient.Complete(
		context.Background(),
		llm.Context{Messages: []llm.Message{llm.NewTextMessage("hello")}},
		llm.StreamOptions{
			APIKey: "secret",
			TransformRequest: func(_ context.Context, _ llm.RequestInfo, payload llm.RequestPayload) (llm.RequestPayload, error) {
				payload.Body = json.RawMessage(`{"model":"replacement"}`)
				return payload, nil
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if assistantReply.StopReason != llm.StopReasonError ||
		assistantReply.ErrorMessage != "request transform cannot replace inspection-only Body; use Set" {
		t.Fatalf("got reply %#v", assistantReply)
	}
	if transportCalled.Load() {
		t.Fatal("request body replacement reached transport")
	}
}

func TestDeepSeekCompatibilityOmitsReasoningEffortWhenThinkingIsOff(t *testing.T) {
	requestBody := make(chan map[string]any, 1)
	testServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		requestBody <- body
		writer.Header().Set("Content-Type", "text/event-stream")
		writeSSE(writer, `{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`)
		fmt.Fprint(writer, "data: [DONE]\n\n")
	}))
	defer testServer.Close()

	targetModel := model(testServer.URL)
	targetModel.Reasoning = true
	targetModel.ReasoningLevels = []llm.ReasoningLevel{llm.ReasoningHigh, llm.ReasoningMax}
	compatibleBehavior := openaiadapter.Compatibility{
		MaxTokensField:  openaiadapter.MaxTokensLegacy,
		ReasoningFormat: openaiadapter.ReasoningFormatDeepSeek,
	}
	llmClient := newConfiguredChatClient(t, targetModel, testServer.Client(), openaiadapter.WithCompatibility(compatibleBehavior))
	assistantReply, err := llmClient.Complete(
		context.Background(),
		llm.Context{Messages: []llm.Message{llm.NewTextMessage("hello")}},
		llm.StreamOptions{APIKey: "secret", Reasoning: llm.ReasoningOff},
	)
	if err != nil || llm.Text(assistantReply) != "ok" {
		t.Fatalf("reply=%#v err=%v", assistantReply, err)
	}
	body := <-requestBody
	thinking := body["thinking"].(map[string]any)
	if thinking["type"] != "disabled" {
		t.Fatalf("got thinking control %#v", thinking)
	}
	if _, exists := body["reasoning_effort"]; exists {
		t.Fatalf("thinking-off request included reasoning_effort: %#v", body)
	}
}

func intPointer(value int) *int { return &value }

func newConfiguredChatClient(
	t *testing.T,
	targetModel llm.Model,
	httpClient *http.Client,
	adapterOptions ...openaiadapter.AdapterOption,
) *llm.Client {
	t.Helper()
	llmClient, err := llmfactory.NewClient(
		targetModel,
		llmfactory.WithHTTPClient(httpClient),
		llmfactory.WithOpenAIAdapterOptions(adapterOptions...),
	)
	if err != nil {
		t.Fatal(err)
	}
	return llmClient
}
