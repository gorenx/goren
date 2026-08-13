package openai_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gorenx/goren/llm"
	openaiadapter "github.com/gorenx/goren/llm/adapter/openai"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestChatCompletionsStreamsTextAndMapsStructuredRequest(t *testing.T) {
	requestBody := make(chan map[string]any, 1)
	testServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, httpRequest *http.Request) {
		if httpRequest.URL.Path != "/v1/chat/completions" {
			t.Errorf("got path %q", httpRequest.URL.Path)
		}
		if authorization := httpRequest.Header.Get("Authorization"); authorization != "Bearer secret" {
			t.Errorf("got authorization %q", authorization)
		}
		var body map[string]any
		if err := json.NewDecoder(httpRequest.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		requestBody <- body

		writer.Header().Set("Content-Type", "text/event-stream")
		writeSSE(writer, `{"id":"response-1","model":"served-model","choices":[{"delta":{"content":"hel"}}]}`)
		writeSSE(writer, `{"choices":[{"delta":{"content":"lo"},"finish_reason":"stop"}]}`)
		writeSSE(writer, `{"choices":[],"usage":{"prompt_tokens":12,"completion_tokens":2,"total_tokens":14,"prompt_tokens_details":{"cached_tokens":3}}}`)
		fmt.Fprint(writer, "data: [DONE]\n\n")
	}))
	defer testServer.Close()

	llmClient := newClient(t, testServer.Client())
	responseStream, err := llmClient.Stream(
		context.Background(),
		model(testServer.URL),
		llm.Context{
			SystemPrompt: "Return JSON.",
			Messages:     []llm.Message{llm.NewTextMessage("hello")},
		},
		llm.StreamOptions{
			APIKey: "secret",
			ResponseFormat: &llm.JSONSchemaFormat{
				Name:   "answer",
				Schema: json.RawMessage(`{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"]}`),
				Strict: true,
			},
		})
	if err != nil {
		t.Fatal(err)
	}

	var events []llm.Event
	for {
		streamedEvent, ok, err := responseStream.Next(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		events = append(events, streamedEvent)
	}
	assistantReply, err := responseStream.Result(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if visibleText := llm.Text(assistantReply); visibleText != "hello" {
		t.Fatalf("got text %q", visibleText)
	}
	if assistantReply.ResponseID != "response-1" || assistantReply.ResponseModel != "served-model" {
		t.Fatalf("missing response identity: %+v", assistantReply)
	}
	if assistantReply.Usage.InputTokens != 9 || assistantReply.Usage.CacheReadTokens != 3 || assistantReply.Usage.OutputTokens != 2 {
		t.Fatalf("got usage %+v", assistantReply.Usage)
	}
	assertEventTypes(t, events,
		llm.StartEvent{},
		llm.TextStartEvent{},
		llm.TextDeltaEvent{},
		llm.TextDeltaEvent{},
		llm.TextEndEvent{},
		llm.DoneEvent{},
	)

	body := <-requestBody
	if body["model"] != "requested-model" || body["stream"] != true {
		t.Fatalf("unexpected request: %#v", body)
	}
	schemaEnvelope, ok := body["response_format"].(map[string]any)
	if !ok || schemaEnvelope["type"] != "json_schema" {
		t.Fatalf("missing structured response format: %#v", body["response_format"])
	}
}

func TestChatCompletionsReassemblesToolCall(t *testing.T) {
	testServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writeSSE(writer, `{"id":"response-2","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call-1","function":{"name":"lookup","arguments":"{\"q\":"}}]}}]}`)
		writeSSE(writer, `{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"memory\"}"}}]},"finish_reason":"tool_calls"}]}`)
		fmt.Fprint(writer, "data: [DONE]\n\n")
	}))
	defer testServer.Close()

	llmClient := newClient(t, testServer.Client())
	assistantReply, err := llmClient.Complete(context.Background(), model(testServer.URL), llm.Context{
		Messages: []llm.Message{llm.NewTextMessage("look up memory")},
		Tools: []llm.Tool{{
			Name:       "lookup",
			Parameters: json.RawMessage(`{"type":"object"}`),
			Strict:     true,
		}},
	}, llm.StreamOptions{APIKey: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if assistantReply.StopReason != llm.StopReasonToolUse || len(assistantReply.Content) != 1 {
		t.Fatalf("unexpected message: %+v", assistantReply)
	}
	assembledCall, ok := assistantReply.Content[0].(llm.ToolCall)
	if !ok {
		t.Fatalf("got content %T", assistantReply.Content[0])
	}
	if assembledCall.ID != "call-1" || assembledCall.Name != "lookup" || string(assembledCall.Arguments) != `{"q":"memory"}` {
		t.Fatalf("got tool call %+v", assembledCall)
	}
}

func TestHTTPFailureIsTerminalStreamEvent(t *testing.T) {
	testServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Error(writer, "rate limited", http.StatusTooManyRequests)
	}))
	defer testServer.Close()

	llmClient := newClient(t, testServer.Client())
	responseStream, err := llmClient.Stream(context.Background(), model(testServer.URL), llm.Context{
		Messages: []llm.Message{llm.NewTextMessage("hello")},
	}, llm.StreamOptions{APIKey: "secret"})
	if err != nil {
		t.Fatalf("unexpected startup error: %v", err)
	}
	assistantReply, err := responseStream.Result(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if assistantReply.StopReason != llm.StopReasonError || assistantReply.ErrorMessage == "" {
		t.Fatalf("unexpected terminal message: %+v", assistantReply)
	}
	terminalEvent, ok, err := responseStream.Next(context.Background())
	if err != nil || !ok {
		t.Fatalf("read event: ok=%v err=%v", ok, err)
	}
	if _, ok := terminalEvent.(llm.ErrorEvent); !ok {
		t.Fatalf("got event %T", terminalEvent)
	}
}

func TestMissingFinishReasonIsTerminalStreamError(t *testing.T) {
	testServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writeSSE(writer, `{"choices":[{"delta":{"content":"partial"}}]}`)
		fmt.Fprint(writer, "data: [DONE]\n\n")
	}))
	defer testServer.Close()

	llmClient := newClient(t, testServer.Client())
	assistantReply, err := llmClient.Complete(context.Background(), model(testServer.URL), llm.Context{
		Messages: []llm.Message{llm.NewTextMessage("hello")},
	}, llm.StreamOptions{APIKey: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if assistantReply.StopReason != llm.StopReasonError || assistantReply.ErrorMessage != "LLM stream ended without finish reason" {
		t.Fatalf("unexpected terminal message: %+v", assistantReply)
	}
}

func TestProviderErrorFinishReasonIsTerminalStreamError(t *testing.T) {
	testServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writeSSE(writer, `{"choices":[{"delta":{},"finish_reason":"content_filter"}]}`)
		fmt.Fprint(writer, "data: [DONE]\n\n")
	}))
	defer testServer.Close()

	llmClient := newClient(t, testServer.Client())
	assistantReply, err := llmClient.Complete(context.Background(), model(testServer.URL), llm.Context{
		Messages: []llm.Message{llm.NewTextMessage("hello")},
	}, llm.StreamOptions{APIKey: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if assistantReply.StopReason != llm.StopReasonError || assistantReply.ErrorMessage != "provider finish reason: content_filter" {
		t.Fatalf("unexpected terminal message: %+v", assistantReply)
	}
}

func TestAPIMismatchFailsBeforeStream(t *testing.T) {
	protocolAdapter := openaiadapter.New(http.DefaultClient)
	wrong := model("https://example.test")
	wrong.API = "different"
	responseStream, err := protocolAdapter.Stream(context.Background(), wrong, llm.Context{
		Messages: []llm.Message{llm.NewTextMessage("hello")},
	}, llm.StreamOptions{APIKey: "secret"})
	if responseStream != nil || !errors.Is(err, llm.ErrAPIMismatch) {
		t.Fatalf("stream=%v err=%v", responseStream, err)
	}
}

func newClient(t *testing.T, httpClient *http.Client) *llm.Client {
	t.Helper()
	adapterRegistry := llm.NewRegistry()
	if err := adapterRegistry.Register(openaiadapter.New(httpClient), "built-in"); err != nil {
		t.Fatal(err)
	}
	llmClient, err := llm.NewClient(adapterRegistry, nil)
	if err != nil {
		t.Fatal(err)
	}
	return llmClient
}

func model(baseURL string) llm.Model {
	return llm.Model{
		ID:              "requested-model",
		Name:            "Requested Model",
		API:             llm.APIOpenAICompletions,
		Provider:        "compatible-provider",
		BaseURL:         baseURL + "/v1",
		Input:           []llm.InputModality{llm.InputText},
		ContextWindow:   16_384,
		MaxOutputTokens: 1_024,
	}
}

func writeSSE(writer http.ResponseWriter, data string) {
	fmt.Fprintf(writer, "data: %s\n\n", data)
	if flusher, ok := writer.(http.Flusher); ok {
		flusher.Flush()
	}
}

func assertEventTypes(t *testing.T, actual []llm.Event, expected ...llm.Event) {
	t.Helper()
	if len(actual) != len(expected) {
		t.Fatalf("got %d events, want %d: %#v", len(actual), len(expected), actual)
	}
	for index := range expected {
		if fmt.Sprintf("%T", actual[index]) != fmt.Sprintf("%T", expected[index]) {
			t.Fatalf("event %d is %T, want %T", index, actual[index], expected[index])
		}
	}
}
