package llmdeepseek

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorenx/goren/llm"
)

type requestSenderFunc func(*http.Request) (*http.Response, error)

func (operation requestSenderFunc) Do(request *http.Request) (*http.Response, error) {
	return operation(request)
}

type faultBody struct {
	content []byte
	offset  int
	closed  bool
}

func (bodyState *faultBody) Read(destination []byte) (int, error) {
	if bodyState.offset < len(bodyState.content) {
		count := copy(destination, bodyState.content[bodyState.offset:])
		bodyState.offset += count
		return count, nil
	}
	return 0, errors.New("connection reset")
}

func (bodyState *faultBody) Close() error {
	bodyState.closed = true
	return nil
}

func TestAdapterStreamsRequestAndPublishesModelCapabilities(t *testing.T) {
	t.Parallel()
	requestChannel := make(chan map[string]any, 1)
	headerChannel := make(chan http.Header, 1)
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			responseWriter.WriteHeader(http.StatusBadRequest)
			return
		}
		requestChannel <- body
		headerChannel <- request.Header.Clone()
		responseWriter.Header().Set("content-type", "text/event-stream")
		_, _ = responseWriter.Write([]byte(
			"data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1}}\n\n" +
				"data: [DONE]\n\n",
		))
	}))
	defer server.Close()

	high := ReasoningHigh
	settings := Config{BaseURL: stringPointer(server.URL), ReasoningEffort: &high}
	connection, err := ResolveOptions(settings, Environment{})
	if err != nil {
		t.Fatal(err)
	}
	var resolutionCount atomic.Int32
	backend, err := NewAdapter(AdapterOptions{
		CurrentOptions: func() (ConnectionOptions, error) {
			resolutionCount.Add(1)
			return connection.Snapshot(), nil
		},
		ResolveAPIKey: func(context.Context, ConnectionOptions) (string, error) { return " test-key ", nil },
		ResolveUserID: func() (string, error) { return "00000000-0000-4000-8000-000000000001", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	chunkFlow, err := backend.Stream(context.Background(), llm.GenerateOptions{
		CallConfig: llm.CallConfig{Provider: ProviderRoute, Model: "deepseek-v4-flash", ReasoningEffort: "high"},
		SessionID:  "session-1", Purpose: llm.PurposeCompaction,
	})
	if err != nil {
		t.Fatal(err)
	}
	assembler := llm.NewBlockAssembler()
	for {
		entry, available, nextErr := chunkFlow.Next(context.Background())
		if nextErr != nil {
			t.Fatal(nextErr)
		}
		if !available {
			break
		}
		if err := assembler.Push(entry); err != nil {
			t.Fatal(err)
		}
	}
	blocks, err := assembler.AssembledBlocks()
	if err != nil || len(blocks) != 1 || blocks[0].(llm.TextBlock).Text != "hello" ||
		assembler.FinishValue().ReasonKind() != "stop" || resolutionCount.Load() != 1 {
		t.Fatalf("assembled = (%#v, %#v, %d, %v)", blocks, assembler.FinishValue(), resolutionCount.Load(), err)
	}
	body := <-requestChannel
	if body["model"] != "deepseek-v4-flash" || body["stream"] != true || body["reasoning_effort"] != "high" {
		t.Fatalf("wire body = %#v", body)
	}
	headers := <-headerChannel
	if headers.Get("authorization") != "Bearer test-key" ||
		headers.Get("user-agent") != userAgent(DefaultHarnessVersion) ||
		headers.Get("x-deepseek-harness-user-id") != "00000000-0000-4000-8000-000000000001" ||
		headers.Get("x-deepseek-harness-session-id") != "session-1" || headers.Get("x-deepseek-harness-compact") != "1" {
		t.Fatalf("wire headers = %#v", headers)
	}

	models, err := backend.ListModels(context.Background(), ProviderRoute)
	if err != nil || len(models) != 2 || models[0].InputModalities[0] != llm.ModalityText {
		t.Fatalf("models = (%#v, %v)", models, err)
	}
	resolved, err := backend.ResolveModel(context.Background(), ProviderRoute, "unlisted")
	if err != nil || resolved.Context == nil || resolved.Context.ContextWindow != DefaultContextWindow ||
		resolved.Reasoning == nil || resolved.Reasoning.DefaultEffort != "high" {
		t.Fatalf("resolved model = (%#v, %v)", resolved, err)
	}
}

func TestAdapterHTTPFailureCarriesStructuredFacts(t *testing.T) {
	t.Parallel()
	now := time.Date(2027, 1, 15, 12, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.Header().Set("retry-after", now.Add(3*time.Second).Format(http.TimeFormat))
		responseWriter.Header().Set("x-deepseek-request-id", "request-1")
		responseWriter.WriteHeader(http.StatusTooManyRequests)
		_, _ = responseWriter.Write([]byte(`{"error":{"message":"credits exhausted","code":"insufficient_quota"}}`))
	}))
	defer server.Close()
	backend := mustAdapter(t, server.URL, 0, func() string { return "key" }, now)
	chunkFlow, err := backend.Stream(context.Background(), llm.GenerateOptions{CallConfig: llm.CallConfig{Model: "m"}})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = chunkFlow.Next(context.Background())
	var providerFailure *llm.LlmError
	if !errors.As(err, &providerFailure) {
		t.Fatalf("HTTP error = %#v", err)
	}
	failure := providerFailure.Failure()
	if failure.Code != llm.QuotaExceededCode || failure.Status == nil || *failure.Status != http.StatusTooManyRequests ||
		failure.ProviderRetryAfterMS == nil || *failure.ProviderRetryAfterMS != 3_000 || failure.RequestID != "request-1" {
		t.Fatalf("HTTP failure = %#v", failure)
	}
}

func TestHTTPErrorCodeClassification(t *testing.T) {
	t.Parallel()
	tests := []struct {
		status int
		detail *wireErrorDetail
		want   string
	}{
		{status: 401, want: "AUTH"},
		{status: 429, want: "RATE_LIMIT"},
		{status: 429, detail: &wireErrorDetail{Code: "insufficient_quota"}, want: llm.QuotaExceededCode},
		{status: 400, detail: &wireErrorDetail{Message: "request too large for model context"}, want: llm.ContextWindowExceededCode},
		{status: 400, detail: &wireErrorDetail{Message: "temperature exceeds maximum allowed value"}, want: "INVALID_REQUEST"},
		{status: 503, want: "SERVER"},
		{status: 418, want: "HTTP_418"},
	}
	for _, testCase := range tests {
		if got := HTTPErrorCode(testCase.status, testCase.detail); got != testCase.want {
			t.Errorf("HTTPErrorCode(%d, %#v) = %q, want %q", testCase.status, testCase.detail, got, testCase.want)
		}
	}
}

func TestAdapterRejectsUnsendableKeyBeforeIO(t *testing.T) {
	t.Parallel()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	defer server.Close()
	secret := "sk-😀supersecret"
	backend := mustAdapter(t, server.URL, 0, func() string { return secret }, time.Now())
	chunkFlow, err := backend.Stream(context.Background(), llm.GenerateOptions{CallConfig: llm.CallConfig{Model: "m"}})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = chunkFlow.Next(context.Background())
	var providerFailure *llm.LlmError
	if !errors.As(err, &providerFailure) || providerFailure.Code() != llm.InvalidCredentialCode ||
		strings.Contains(providerFailure.Error(), secret) || strings.Contains(providerFailure.Error(), "supersecret") || requests.Load() != 0 {
		t.Fatalf("credential refusal = (%#v, requests=%d)", err, requests.Load())
	}
}

func TestAdapterClassifiesCancellationAndIdleTimeout(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		responseWriter.Header().Set("content-type", "text/event-stream")
		responseWriter.WriteHeader(http.StatusOK)
		if flusher, ok := responseWriter.(http.Flusher); ok {
			flusher.Flush()
		}
		<-request.Context().Done()
	}))
	defer server.Close()
	backend := mustAdapter(t, server.URL, 20*time.Millisecond, func() string { return "key" }, time.Now())
	chunkFlow, err := backend.Stream(context.Background(), llm.GenerateOptions{CallConfig: llm.CallConfig{Model: "m"}})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = chunkFlow.Next(context.Background())
	var providerFailure *llm.LlmError
	if !errors.As(err, &providerFailure) || providerFailure.Code() != "TIMEOUT" {
		t.Fatalf("idle error = %#v", err)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	chunkFlow, err = backend.Stream(context.Background(), llm.GenerateOptions{CallConfig: llm.CallConfig{Model: "m"}})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = chunkFlow.Next(cancelled)
	if !errors.As(err, &providerFailure) || providerFailure.Code() != "ABORTED" {
		t.Fatalf("cancel error = %#v", err)
	}
}

func TestAdapterClassifiesMidStreamReadFailureAsTransportAndTerminates(t *testing.T) {
	t.Parallel()
	baseURL := "https://gateway.example/path"
	connection, err := ResolveOptions(Config{BaseURL: &baseURL}, Environment{})
	if err != nil {
		t.Fatal(err)
	}
	bodyState := &faultBody{content: []byte(`data: {"choices":[{"delta":{"content":"partial"}}]}` + "\n\n")}
	backend, err := NewAdapter(AdapterOptions{
		CurrentOptions: func() (ConnectionOptions, error) { return connection.Snapshot(), nil },
		ResolveAPIKey:  func(context.Context, ConnectionOptions) (string, error) { return "key", nil },
		ResolveUserID:  func() (string, error) { return "00000000-0000-4000-8000-000000000001", nil },
		RequestSender: requestSenderFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: bodyState}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	chunkFlow, err := backend.Stream(context.Background(), llm.GenerateOptions{CallConfig: llm.CallConfig{Model: "m"}})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, available, nextErr := chunkFlow.Next(context.Background()); nextErr != nil || !available {
			t.Fatalf("partial chunk = (available=%t, error=%v)", available, nextErr)
		}
	}
	_, _, err = chunkFlow.Next(context.Background())
	var providerFailure *llm.LlmError
	if !errors.As(err, &providerFailure) || providerFailure.Code() != "TRANSPORT" ||
		providerFailure.Error() != "DeepSeek API stream from https://gateway.example/path failed" || !bodyState.closed {
		t.Fatalf("stream failure = (%#v, body closed=%t)", err, bodyState.closed)
	}
	if _, available, nextErr := chunkFlow.Next(context.Background()); nextErr != nil || available {
		t.Fatalf("post-error stream = (available=%t, error=%v)", available, nextErr)
	}
}

func TestAdapterMetadataIsDetached(t *testing.T) {
	t.Parallel()
	backend := mustAdapter(t, "http://127.0.0.1:1", 0, func() string { return "key" }, time.Now())
	first, err := backend.ListModels(context.Background(), ProviderRoute)
	if err != nil {
		t.Fatal(err)
	}
	first[0].Name = "changed"
	second, err := backend.ListModels(context.Background(), ProviderRoute)
	if err != nil {
		t.Fatal(err)
	}
	if reflect.DeepEqual(first, second) || second[0].Name == "changed" {
		t.Fatalf("model metadata retained aliases: first=%#v second=%#v", first, second)
	}
}

func mustAdapter(t *testing.T, baseURL string, idleTimeout time.Duration, key func() string, now time.Time) *Adapter {
	t.Helper()
	settings := Config{BaseURL: stringPointer(baseURL)}
	if idleTimeout > 0 {
		milliseconds := float64(idleTimeout) / float64(time.Millisecond)
		settings.StreamIdleTimeoutMS = &milliseconds
	}
	connection, err := ResolveOptions(settings, Environment{})
	if err != nil {
		t.Fatal(err)
	}
	backend, err := NewAdapter(AdapterOptions{
		CurrentOptions: func() (ConnectionOptions, error) { return connection.Snapshot(), nil },
		ResolveAPIKey:  func(context.Context, ConnectionOptions) (string, error) { return key(), nil },
		ResolveUserID:  func() (string, error) { return "00000000-0000-4000-8000-000000000001", nil },
		Now:            func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	return backend
}
