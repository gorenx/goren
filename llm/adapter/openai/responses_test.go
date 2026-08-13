package openai_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorenx/goren/llm"
	openaiadapter "github.com/gorenx/goren/llm/adapter/openai"
)

func TestResponsesStreamsReasoningAndTextAndMapsRequest(t *testing.T) {
	requestBody := make(chan map[string]any, 1)
	testServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, httpRequest *http.Request) {
		if httpRequest.URL.Path != "/v1/responses" {
			t.Errorf("got path %q", httpRequest.URL.Path)
		}
		if authorization := httpRequest.Header.Get("Authorization"); authorization != "Bearer secret" {
			t.Errorf("got authorization %q", authorization)
		}
		if configured := httpRequest.Header.Get("X-Model-Config"); configured != "original" {
			t.Errorf("got model header %q", configured)
		}
		if invocation := httpRequest.Header.Get("X-Invocation"); invocation != "request" {
			t.Errorf("got invocation header %q", invocation)
		}
		var body map[string]any
		if err := json.NewDecoder(httpRequest.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		requestBody <- body

		writer.Header().Set("Content-Type", "text/event-stream")
		writeSSE(writer, `{"type":"response.created","response":{"id":"resp-1","model":"served-model"}}`)
		writeSSE(writer, `{"type":"response.output_item.added","output_index":0,"item":{"id":"rs-1","type":"reasoning","summary":[]}}`)
		writeSSE(writer, `{"type":"response.reasoning_summary_text.delta","item_id":"rs-1","delta":"considered"}`)
		writeSSE(writer, `{"type":"response.output_item.done","output_index":0,"item":{"id":"rs-1","type":"reasoning","summary":[{"type":"summary_text","text":"considered"}],"encrypted_content":"encrypted"}}`)
		writeSSE(writer, `{"type":"response.output_item.added","output_index":1,"item":{"id":"msg-1","type":"message","role":"assistant","phase":"final_answer","status":"in_progress","content":[]}}`)
		writeSSE(writer, `{"type":"response.output_text.delta","item_id":"msg-1","output_index":1,"content_index":0,"delta":"hel"}`)
		writeSSE(writer, `{"type":"response.output_text.delta","item_id":"msg-1","output_index":1,"content_index":0,"delta":"lo"}`)
		writeSSE(writer, `{"type":"response.output_item.done","output_index":1,"item":{"id":"msg-1","type":"message","role":"assistant","phase":"final_answer","status":"completed","content":[{"type":"output_text","text":"hello","annotations":[]}]}}`)
		writeSSE(writer, `{"type":"response.completed","response":{"id":"resp-1","model":"served-model","status":"completed","usage":{"input_tokens":15,"output_tokens":4,"total_tokens":19,"input_tokens_details":{"cached_tokens":3,"cache_write_tokens":2},"output_tokens_details":{"reasoning_tokens":1}}}}`)
	}))
	defer testServer.Close()

	targetModel := responsesModel(testServer.URL)
	targetModel.Reasoning = true
	targetModel.Input = []llm.InputModality{llm.InputText, llm.InputImage}
	targetModel.Headers = map[string]string{"X-Model-Config": "original"}
	llmClient := newResponsesClient(t, targetModel, testServer.Client())
	targetModel.ID = "mutated-after-construction"
	targetModel.Headers["X-Model-Config"] = "mutated"

	previousReasoning := `{"id":"rs-prior","type":"reasoning","summary":[{"type":"summary_text","text":"prior thought"}],"encrypted_content":"prior-encrypted"}`
	textReplay := &llm.ReplayMetadata{API: llm.APIOpenAIResponses, Provider: "compatible-provider", Model: "requested-model", Data: json.RawMessage(`{"item_id":"msg-prior"}`)}
	toolReplay := &llm.ReplayMetadata{API: llm.APIOpenAIResponses, Provider: "compatible-provider", Model: "requested-model", Data: json.RawMessage(`{"item_id":"fc-prior"}`)}
	responseStream, err := llmClient.Stream(context.Background(), llm.Context{
		SystemPrompt: "Return JSON.",
		Messages: []llm.Message{
			llm.UserMessage{Content: []llm.UserContent{
				llm.TextContent{Text: "look"},
				llm.ImageContent{MIMEType: "image/png", Data: "aW1hZ2U="},
			}},
			llm.AssistantMessage{
				API:      llm.APIOpenAIResponses,
				Provider: "compatible-provider",
				Model:    "requested-model",
				Content: []llm.AssistantContent{
					llm.ThinkingContent{Thinking: "prior thought", Signature: previousReasoning},
					llm.AssistantTextContent{Text: "I need a tool.", Phase: llm.AssistantTextPhaseCommentary, Metadata: textReplay},
					llm.ToolCall{ID: "call|opaque", Name: "lookup", Arguments: json.RawMessage(`{"q":"memory"}`), Metadata: toolReplay},
				},
				StopReason: llm.StopReasonToolUse,
			},
			llm.ToolResultMessage{
				ToolCallID: "call|opaque",
				ToolName:   "lookup",
				IsError:    true,
				Content: []llm.ToolResultContent{
					llm.TextContent{Text: "found"},
					llm.ImageContent{MIMEType: "image/png", Data: "cmVzdWx0"},
				},
			},
			llm.NewTextMessage("answer"),
		},
		Tools: []llm.Tool{{
			Name:        "lookup",
			Description: "Find memory",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`),
			Strict:      true,
		}},
	}, llm.StreamOptions{
		APIKey:     "secret",
		Reasoning:  llm.ReasoningMedium,
		ToolChoice: &llm.ToolChoice{Mode: llm.ToolChoiceFunction, Name: "lookup"},
		Headers:    map[string]string{"X-Invocation": "request"},
		ResponseFormat: &llm.JSONSchemaFormat{
			Name:        "answer",
			Description: "Answer envelope",
			Schema:      json.RawMessage(`{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"]}`),
			Strict:      true,
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
	if assistantReply.ResponseID != "resp-1" || assistantReply.ResponseModel != "served-model" {
		t.Fatalf("missing response identity: %+v", assistantReply)
	}
	if assistantReply.Usage.InputTokens != 10 ||
		assistantReply.Usage.CacheReadTokens != 3 ||
		assistantReply.Usage.CacheWriteTokens != 2 ||
		assistantReply.Usage.OutputTokens != 4 ||
		assistantReply.Usage.TotalTokens != 19 {
		t.Fatalf("got usage %+v", assistantReply.Usage)
	}
	if len(assistantReply.Content) != 2 {
		t.Fatalf("got content %#v", assistantReply.Content)
	}
	thinking, ok := assistantReply.Content[0].(llm.ThinkingContent)
	if !ok || thinking.Thinking != "considered" || !json.Valid([]byte(thinking.Signature)) {
		t.Fatalf("got thinking %#v", assistantReply.Content[0])
	}
	visible, ok := assistantReply.Content[1].(llm.AssistantTextContent)
	if !ok || visible.Phase != llm.AssistantTextPhaseFinalAnswer || visible.Metadata == nil || string(visible.Metadata.Data) != `{"item_id":"msg-1"}` {
		t.Fatalf("got text replay metadata %#v", assistantReply.Content[1])
	}
	assertEventTypes(t, events,
		llm.StartEvent{},
		llm.ThinkingStartEvent{},
		llm.ThinkingDeltaEvent{},
		llm.ThinkingEndEvent{},
		llm.TextStartEvent{},
		llm.TextDeltaEvent{},
		llm.TextDeltaEvent{},
		llm.TextEndEvent{},
		llm.DoneEvent{},
	)

	body := <-requestBody
	if body["model"] != "requested-model" || body["stream"] != true || body["store"] != false {
		t.Fatalf("unexpected request controls: %#v", body)
	}
	if body["max_output_tokens"] != float64(1_024) {
		t.Fatalf("got max output tokens %#v", body["max_output_tokens"])
	}
	reasoning, ok := body["reasoning"].(map[string]any)
	if !ok || reasoning["effort"] != "medium" || reasoning["summary"] != "auto" {
		t.Fatalf("got reasoning %#v", body["reasoning"])
	}
	include, ok := body["include"].([]any)
	if !ok || len(include) != 1 || include[0] != "reasoning.encrypted_content" {
		t.Fatalf("got include %#v", body["include"])
	}
	textConfig, ok := body["text"].(map[string]any)
	if !ok {
		t.Fatalf("got text config %#v", body["text"])
	}
	format, ok := textConfig["format"].(map[string]any)
	if !ok || format["type"] != "json_schema" || format["name"] != "answer" || format["strict"] != true {
		t.Fatalf("got response format %#v", textConfig["format"])
	}
	tools, ok := body["tools"].([]any)
	if !ok || len(tools) != 1 || tools[0].(map[string]any)["strict"] != true {
		t.Fatalf("got tools %#v", body["tools"])
	}
	toolChoice, ok := body["tool_choice"].(map[string]any)
	if !ok || toolChoice["type"] != "function" || toolChoice["name"] != "lookup" {
		t.Fatalf("got tool choice %#v", body["tool_choice"])
	}
	assertResponsesInput(t, body["input"])
}

func TestResponsesReassemblesToolCall(t *testing.T) {
	testServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writeSSE(writer, `{"type":"response.created","response":{"id":"resp-2","model":"served-model"}}`)
		writeSSE(writer, `{"type":"response.output_item.added","output_index":0,"item":{"id":"fc-2","type":"function_call","call_id":"call-2","name":"lookup","arguments":""}}`)
		writeSSE(writer, `{"type":"response.function_call_arguments.delta","item_id":"fc-2","output_index":0,"delta":"{\"q\":"}`)
		writeSSE(writer, `{"type":"response.function_call_arguments.done","item_id":"fc-2","output_index":0,"name":"lookup","arguments":"{\"q\":\"memory\"}"}`)
		writeSSE(writer, `{"type":"response.output_item.done","output_index":0,"item":{"id":"fc-2","type":"function_call","call_id":"call-2","name":"lookup","arguments":"{\"q\":\"memory\"}","status":"completed"}}`)
		writeSSE(writer, `{"type":"response.completed","response":{"id":"resp-2","model":"served-model","status":"completed"}}`)
	}))
	defer testServer.Close()

	llmClient := newResponsesClient(t, responsesModel(testServer.URL), testServer.Client())
	assistantReply, err := llmClient.Complete(context.Background(), llm.Context{
		Messages: []llm.Message{llm.NewTextMessage("look up memory")},
		Tools: []llm.Tool{{
			Name:       "lookup",
			Parameters: json.RawMessage(`{"type":"object"}`),
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
	if assembledCall.ID != "call-2" || assembledCall.Name != "lookup" || string(assembledCall.Arguments) != `{"q":"memory"}` {
		t.Fatalf("got tool call %+v", assembledCall)
	}
}

func TestResponsesRejectsMalformedSameModelReplayMetadata(t *testing.T) {
	targetModel := responsesModel("https://example.test")
	llmClient := newResponsesClient(t, targetModel, http.DefaultClient)
	_, err := llmClient.Stream(context.Background(), llm.Context{Messages: []llm.Message{
		llm.AssistantMessage{
			API: targetModel.API, Provider: targetModel.Provider, Model: targetModel.ID,
			StopReason: llm.StopReasonStop,
			Content: []llm.AssistantContent{llm.AssistantTextContent{
				Text: "prior", Metadata: &llm.ReplayMetadata{
					API: targetModel.API, Provider: targetModel.Provider, Model: targetModel.ID,
					Data: json.RawMessage(`{"unexpected":"value"}`),
				},
			}},
		},
	}}, llm.StreamOptions{APIKey: "secret"})
	if err == nil || !strings.Contains(err.Error(), "replay metadata has no item ID") {
		t.Fatalf("got replay metadata error %v", err)
	}
}

func TestResponsesIncompleteMaxOutputTokensMapsToLength(t *testing.T) {
	testServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writeSSE(writer, `{"type":"response.incomplete","response":{"id":"resp-3","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}`)
	}))
	defer testServer.Close()

	llmClient := newResponsesClient(t, responsesModel(testServer.URL), testServer.Client())
	assistantReply, err := llmClient.Complete(context.Background(), llm.Context{
		Messages: []llm.Message{llm.NewTextMessage("hello")},
	}, llm.StreamOptions{APIKey: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if assistantReply.StopReason != llm.StopReasonLength {
		t.Fatalf("unexpected terminal message: %+v", assistantReply)
	}
}

func TestResponsesFailureIsTerminalStreamError(t *testing.T) {
	testServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writeSSE(writer, `{"type":"response.created","response":{"id":"resp-4","model":"served-model"}}`)
		writeSSE(writer, `{"type":"response.output_item.added","output_index":0,"item":{"id":"msg-partial","type":"message","role":"assistant","status":"in_progress","content":[]}}`)
		writeSSE(writer, `{"type":"response.output_text.delta","item_id":"msg-partial","output_index":0,"content_index":0,"delta":"partial"}`)
		writeSSE(writer, `{"type":"response.failed","response":{"id":"resp-4","model":"served-model","status":"failed","usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7},"error":{"code":"server_error","message":"upstream failed"}}}`)
	}))
	defer testServer.Close()

	llmClient := newResponsesClient(t, responsesModel(testServer.URL), testServer.Client())
	assistantReply, err := llmClient.Complete(context.Background(), llm.Context{
		Messages: []llm.Message{llm.NewTextMessage("hello")},
	}, llm.StreamOptions{APIKey: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if assistantReply.StopReason != llm.StopReasonError ||
		assistantReply.ErrorMessage != "OpenAI response failed (server_error): upstream failed" {
		t.Fatalf("unexpected terminal message: %+v", assistantReply)
	}
	if llm.Text(assistantReply) != "partial" || assistantReply.ResponseID != "resp-4" || assistantReply.ResponseModel != "served-model" || assistantReply.Usage.TotalTokens != 7 {
		t.Fatalf("runtime error lost partial state: %+v", assistantReply)
	}
}

func TestResponsesAPIMismatchFailsAtAdapterConstruction(t *testing.T) {
	wrong := responsesModel("https://example.test")
	wrong.API = llm.APIOpenAICompletions
	protocolAdapter, err := openaiadapter.NewResponses(wrong, http.DefaultClient)
	if protocolAdapter != nil || !errors.Is(err, llm.ErrAPIMismatch) {
		t.Fatalf("adapter=%v err=%v", protocolAdapter, err)
	}
}

func assertResponsesInput(t *testing.T, rawInput any) {
	t.Helper()
	items, ok := rawInput.([]any)
	if !ok || len(items) != 7 {
		t.Fatalf("got input %#v", rawInput)
	}
	item := func(index int) map[string]any {
		t.Helper()
		mapped, ok := items[index].(map[string]any)
		if !ok {
			t.Fatalf("input %d is %#v", index, items[index])
		}
		return mapped
	}
	if item(0)["role"] != "developer" || item(0)["content"] != "Return JSON." {
		t.Fatalf("got system input %#v", item(0))
	}
	userContent, ok := item(1)["content"].([]any)
	if !ok || len(userContent) != 2 || userContent[0].(map[string]any)["type"] != "input_text" || userContent[1].(map[string]any)["type"] != "input_image" {
		t.Fatalf("got user input %#v", item(1))
	}
	if item(2)["type"] != "reasoning" || item(2)["id"] != "rs-prior" {
		t.Fatalf("got reasoning replay %#v", item(2))
	}
	if item(3)["type"] != "message" || item(3)["role"] != "assistant" || item(3)["id"] != "msg-prior" || item(3)["phase"] != "commentary" {
		t.Fatalf("got assistant replay %#v", item(3))
	}
	if item(4)["type"] != "function_call" || item(4)["call_id"] != "call|opaque" || item(4)["id"] != "fc-prior" {
		t.Fatalf("got function call replay %#v", item(4))
	}
	if item(5)["type"] != "function_call_output" || item(5)["call_id"] != "call|opaque" {
		t.Fatalf("got function output %#v", item(5))
	}
	output, ok := item(5)["output"].([]any)
	if !ok || len(output) != 2 || output[0].(map[string]any)["type"] != "input_text" || output[1].(map[string]any)["type"] != "input_image" {
		t.Fatalf("got function output content %#v", item(5)["output"])
	}
	if output[0].(map[string]any)["text"] != "[tool_error] found" {
		t.Fatalf("got tool error output %#v", output[0])
	}
	if item(6)["role"] != "user" {
		t.Fatalf("got latest user input %#v", item(6))
	}
}

func newResponsesClient(t *testing.T, targetModel llm.Model, httpClient *http.Client) *llm.Client {
	t.Helper()
	adapterRegistry := llm.NewRegistry()
	if err := adapterRegistry.Register(
		llm.APIOpenAIResponses,
		func(targetModel llm.Model) (llm.APIAdapter, error) {
			return openaiadapter.NewResponses(targetModel, httpClient)
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

func responsesModel(baseURL string) llm.Model {
	return llm.Model{
		ID:              "requested-model",
		Name:            "Requested Model",
		API:             llm.APIOpenAIResponses,
		Provider:        "compatible-provider",
		BaseURL:         baseURL + "/v1",
		Input:           []llm.InputModality{llm.InputText},
		ContextWindow:   16_384,
		MaxOutputTokens: 1_024,
	}
}

func ExampleNewResponses() {
	adapterRegistry := llm.NewRegistry()
	_ = adapterRegistry.Register(llm.APIOpenAIResponses, func(targetModel llm.Model) (llm.APIAdapter, error) {
		return openaiadapter.NewResponses(targetModel, nil)
	})
	fmt.Println(adapterRegistry.APIs())
	// Output: [openai-responses]
}
