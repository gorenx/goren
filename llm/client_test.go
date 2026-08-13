package llm_test

import (
	"context"
	"errors"
	"github.com/gorenx/goren/llm"
	"testing"
	"time"
)

type scriptedAdapter struct {
	api      llm.API
	response string
	failure  error
	calls    int
}

func (script *scriptedAdapter) API() llm.API {
	return script.api
}

func (script *scriptedAdapter) Stream(
	ctx context.Context,
	targetModel llm.Model,
	_ llm.Context,
	_ llm.StreamOptions,
) (*llm.EventStream, error) {
	script.calls++
	return llm.NewEventStream(ctx, targetModel, func(_ context.Context, eventSink llm.StreamEmitter) {
		if script.failure != nil {
			eventSink.Fail(script.failure)
			return
		}
		eventSink.Emit(llm.StartEvent{})
		eventSink.Emit(llm.TextStartEvent{ContentIndex: 0})
		eventSink.Emit(llm.TextDeltaEvent{ContentIndex: 0, Delta: script.response})
		eventSink.Emit(llm.TextEndEvent{ContentIndex: 0, Content: script.response})
		eventSink.Done(llm.AssistantMessage{
			Content:    []llm.AssistantContent{llm.TextContent{Text: script.response}},
			API:        targetModel.API,
			Provider:   targetModel.Provider,
			Model:      targetModel.ID,
			StopReason: llm.StopReasonStop,
			Timestamp:  time.Now(),
		})
	}), nil
}

func TestClientRoutesByAPINotProvider(t *testing.T) {
	adapterRegistry := llm.NewRegistry()
	script := &scriptedAdapter{api: "shared-protocol", response: "ok"}
	if err := adapterRegistry.Register(script, "test"); err != nil {
		t.Fatal(err)
	}
	llmClient, err := llm.NewClient(adapterRegistry, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, servingProvider := range []llm.Provider{"provider-a", "provider-b"} {
		assistantReply, err := llmClient.Complete(context.Background(), validModel(script.api, servingProvider), validContext(), llm.StreamOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if visibleText := llm.Text(assistantReply); visibleText != "ok" {
			t.Fatalf("got response %q", visibleText)
		}
	}
	if script.calls != 2 {
		t.Fatalf("got %d adapter calls", script.calls)
	}
}

func TestClientReturnsStartupErrorBeforeStream(t *testing.T) {
	llmClient, err := llm.NewClient(llm.NewRegistry(), nil)
	if err != nil {
		t.Fatal(err)
	}
	responseStream, err := llmClient.Stream(context.Background(), validModel("missing", "provider"), validContext(), llm.StreamOptions{})
	if responseStream != nil {
		t.Fatal("expected no stream")
	}
	if !errors.Is(err, llm.ErrAdapterNotRegistered) {
		t.Fatalf("got error %v", err)
	}
}

func TestRuntimeFailureTerminatesReturnedStream(t *testing.T) {
	adapterRegistry := llm.NewRegistry()
	script := &scriptedAdapter{api: "test", failure: errors.New("upstream disconnected")}
	if err := adapterRegistry.Register(script, "test"); err != nil {
		t.Fatal(err)
	}
	llmClient, err := llm.NewClient(adapterRegistry, nil)
	if err != nil {
		t.Fatal(err)
	}
	responseStream, err := llmClient.Stream(context.Background(), validModel("test", "provider"), validContext(), llm.StreamOptions{})
	if err != nil {
		t.Fatalf("stream startup failed: %v", err)
	}
	assistantReply, err := responseStream.Result(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if assistantReply.StopReason != llm.StopReasonError || assistantReply.ErrorMessage != "upstream disconnected" {
		t.Fatalf("unexpected terminal message: %+v", assistantReply)
	}
	terminalEvent, ok, err := responseStream.Next(context.Background())
	if err != nil || !ok {
		t.Fatalf("read terminal event: ok=%v err=%v", ok, err)
	}
	if _, ok := terminalEvent.(llm.ErrorEvent); !ok {
		t.Fatalf("got event %T", terminalEvent)
	}
}

func TestRegistryUnregistersOnlyMatchingSource(t *testing.T) {
	adapterRegistry := llm.NewRegistry()
	first := &scriptedAdapter{api: "first"}
	second := &scriptedAdapter{api: "second"}
	if err := adapterRegistry.Register(first, "plugin-a"); err != nil {
		t.Fatal(err)
	}
	if err := adapterRegistry.Register(second, "plugin-b"); err != nil {
		t.Fatal(err)
	}
	adapterRegistry.UnregisterSource("plugin-a")
	if _, ok := adapterRegistry.Adapter("first"); ok {
		t.Fatal("first adapter remains registered")
	}
	if _, ok := adapterRegistry.Adapter("second"); !ok {
		t.Fatal("second adapter was removed")
	}
}

func TestProducerWithoutTerminalEventBecomesRuntimeError(t *testing.T) {
	targetModel := validModel("test", "provider")
	responseStream := llm.NewEventStream(context.Background(), targetModel, func(context.Context, llm.StreamEmitter) {})
	assistantReply, err := responseStream.Result(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if assistantReply.StopReason != llm.StopReasonError || assistantReply.ErrorMessage != llm.ErrInvalidStream.Error() {
		t.Fatalf("unexpected terminal message: %+v", assistantReply)
	}
}

func validModel(protocol llm.API, servingProvider llm.Provider) llm.Model {
	return llm.Model{
		ID:              "model-1",
		Name:            "Model 1",
		API:             protocol,
		Provider:        servingProvider,
		BaseURL:         "https://example.test/v1",
		Input:           []llm.InputModality{llm.InputText},
		ContextWindow:   8_192,
		MaxOutputTokens: 1_024,
	}
}

func validContext() llm.Context {
	return llm.Context{Messages: []llm.Message{llm.NewTextMessage("hello")}}
}
