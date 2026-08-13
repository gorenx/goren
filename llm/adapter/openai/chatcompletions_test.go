package openai_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorenx/goren/llm"
	openaiadapter "github.com/gorenx/goren/llm/adapter/openai"
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
		if configured := httpRequest.Header.Get("X-Model-Config"); configured != "original" {
			t.Errorf("got model header %q", configured)
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

	targetModel := model(testServer.URL)
	targetModel.Headers = map[string]string{"X-Model-Config": "original"}
	llmClient := newClient(t, targetModel, testServer.Client())
	targetModel.ID = "mutated-after-construction"
	targetModel.Headers["X-Model-Config"] = "mutated"
	responseStream, err := llmClient.Stream(
		context.Background(),
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

	llmClient := newClient(t, model(testServer.URL), testServer.Client())
	assistantReply, err := llmClient.Complete(context.Background(), llm.Context{
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

func TestChatCompletionsNormalizesForeignToolIdentityAtProtocolBoundary(t *testing.T) {
	requestBody := make(chan map[string]any, 1)
	testServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		requestBody <- body
		writer.Header().Set("Content-Type", "text/event-stream")
		writeSSE(writer, `{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`)
		fmt.Fprint(writer, "data: [DONE]\n\n")
	}))
	defer testServer.Close()

	foreignID := "call|foreign item"
	llmClient := newClient(t, model(testServer.URL), testServer.Client())
	_, err := llmClient.Complete(context.Background(), llm.Context{
		Messages: []llm.Message{
			llm.AssistantMessage{
				API: "foreign-api", Provider: "foreign-provider", Model: "foreign-model",
				StopReason: llm.StopReasonToolUse,
				Content: []llm.AssistantContent{
					llm.ToolCall{ID: foreignID, Name: "lookup", Arguments: json.RawMessage(`{}`)},
				},
			},
			llm.ToolResultMessage{
				ToolCallID: foreignID, ToolName: "lookup",
				Content: []llm.ToolResultContent{llm.TextContent{Text: "found"}},
			},
			llm.NewTextMessage("continue"),
		},
		Tools: []llm.Tool{{Name: "lookup", Parameters: json.RawMessage(`{"type":"object"}`)}},
	}, llm.StreamOptions{APIKey: "secret"})
	if err != nil {
		t.Fatal(err)
	}

	messages := (<-requestBody)["messages"].([]any)
	assistantMessage := messages[0].(map[string]any)
	toolCalls := assistantMessage["tool_calls"].([]any)
	callID := toolCalls[0].(map[string]any)["id"]
	toolMessage := messages[1].(map[string]any)
	if callID != "call" || toolMessage["tool_call_id"] != callID {
		t.Fatalf("tool identity was not mapped consistently: call=%#v result=%#v", callID, toolMessage["tool_call_id"])
	}
}

func TestHTTPFailureIsTerminalStreamEvent(t *testing.T) {
	testServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Error(writer, "rate limited", http.StatusTooManyRequests)
	}))
	defer testServer.Close()

	llmClient := newClient(t, model(testServer.URL), testServer.Client())
	responseStream, err := llmClient.Stream(context.Background(), llm.Context{
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

	llmClient := newClient(t, model(testServer.URL), testServer.Client())
	assistantReply, err := llmClient.Complete(context.Background(), llm.Context{
		Messages: []llm.Message{llm.NewTextMessage("hello")},
	}, llm.StreamOptions{APIKey: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if assistantReply.StopReason != llm.StopReasonError || assistantReply.ErrorMessage != "LLM stream ended without finish reason" {
		t.Fatalf("unexpected terminal message: %+v", assistantReply)
	}
	if llm.Text(assistantReply) != "partial" {
		t.Fatalf("runtime error lost partial content: %+v", assistantReply)
	}
}

func TestProviderErrorFinishReasonIsTerminalStreamError(t *testing.T) {
	testServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writeSSE(writer, `{"choices":[{"delta":{},"finish_reason":"content_filter"}]}`)
		fmt.Fprint(writer, "data: [DONE]\n\n")
	}))
	defer testServer.Close()

	llmClient := newClient(t, model(testServer.URL), testServer.Client())
	assistantReply, err := llmClient.Complete(context.Background(), llm.Context{
		Messages: []llm.Message{llm.NewTextMessage("hello")},
	}, llm.StreamOptions{APIKey: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if assistantReply.StopReason != llm.StopReasonError || assistantReply.ErrorMessage != "provider finish reason: content_filter" {
		t.Fatalf("unexpected terminal message: %+v", assistantReply)
	}
}

func TestChatCompletionsMapsToolResultImagesAndEnforcesModelModality(t *testing.T) {
	requestBody := make(chan map[string]any, 1)
	testServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		requestBody <- body
		writer.Header().Set("Content-Type", "text/event-stream")
		writeSSE(writer, `{"choices":[{"delta":{"content":"seen"},"finish_reason":"stop"}]}`)
		fmt.Fprint(writer, "data: [DONE]\n\n")
	}))
	defer testServer.Close()

	targetModel := model(testServer.URL)
	targetModel.Input = []llm.InputModality{llm.InputText, llm.InputImage}
	llmClient := newClient(t, targetModel, testServer.Client())
	call := llm.AssistantMessage{
		API: targetModel.API, Provider: targetModel.Provider, Model: targetModel.ID,
		StopReason: llm.StopReasonToolUse,
		Content:    []llm.AssistantContent{llm.ToolCall{ID: "call-1", Name: "inspect", Arguments: json.RawMessage(`{}`)}},
	}
	_, err := llmClient.Complete(context.Background(), llm.Context{Messages: []llm.Message{
		call,
		llm.ToolResultMessage{ToolCallID: "call-1", ToolName: "inspect", Content: []llm.ToolResultContent{llm.ImageContent{MIMEType: "image/png", Data: "aW1hZ2U="}}},
	}}, llm.StreamOptions{APIKey: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	messages := (<-requestBody)["messages"].([]any)
	if len(messages) != 3 {
		t.Fatalf("got messages %#v", messages)
	}
	imageMessage := messages[2].(map[string]any)
	content := imageMessage["content"].([]any)
	if imageMessage["role"] != "user" || content[1].(map[string]any)["type"] != "image_url" {
		t.Fatalf("got image replay %#v", imageMessage)
	}

	unsupported := model(testServer.URL)
	unsupportedClient := newClient(t, unsupported, testServer.Client())
	_, err = unsupportedClient.Stream(context.Background(), llm.Context{Messages: []llm.Message{
		llm.UserMessage{Content: []llm.UserContent{llm.ImageContent{MIMEType: "image/png", Data: "aW1hZ2U="}}},
	}}, llm.StreamOptions{APIKey: "secret"})
	if !errors.Is(err, llm.ErrUnsupportedModality) {
		t.Fatalf("got unsupported image error %v", err)
	}
}

func TestAPIMismatchFailsAtAdapterConstruction(t *testing.T) {
	wrong := model("https://example.test")
	wrong.API = "different"
	protocolAdapter, err := openaiadapter.New(wrong, http.DefaultClient)
	if protocolAdapter != nil || !errors.Is(err, llm.ErrAPIMismatch) {
		t.Fatalf("adapter=%v err=%v", protocolAdapter, err)
	}
}

func newClient(t *testing.T, targetModel llm.Model, httpClient *http.Client) *llm.Client {
	t.Helper()
	adapterRegistry := llm.NewRegistry()
	if err := adapterRegistry.Register(
		llm.APIOpenAICompletions,
		func(targetModel llm.Model) (llm.APIAdapter, error) {
			return openaiadapter.New(targetModel, httpClient)
		},
	); err != nil {
		t.Fatal(err)
	}
	llmClient, err := llm.NewClient(targetModel, adapterRegistry, nil)
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
