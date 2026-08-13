package llm_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gorenx/goren/llm"
)

type constructorScript struct {
	api           llm.API
	response      string
	failure       error
	createCalls   int
	streamCalls   int
	receivedModel llm.Model
}

func (script *constructorScript) construct(targetModel llm.Model) (llm.APIAdapter, error) {
	if targetModel.API != script.api {
		return nil, llm.ErrAPIMismatch
	}
	script.createCalls++
	script.receivedModel = targetModel
	return &scriptedAdapter{script: script, targetModel: targetModel}, nil
}

type scriptedAdapter struct {
	script      *constructorScript
	targetModel llm.Model
}

func (script *scriptedAdapter) API() llm.API {
	return script.targetModel.API
}

func (script *scriptedAdapter) Stream(
	ctx context.Context,
	_ llm.Context,
	_ llm.StreamOptions,
) (*llm.EventStream, error) {
	script.script.streamCalls++
	return llm.NewEventStream(ctx, script.targetModel, func(_ context.Context, eventSink llm.StreamEmitter) {
		if script.script.failure != nil {
			eventSink.Fail(script.script.failure)
			return
		}
		eventSink.Emit(llm.StartEvent{})
		eventSink.Emit(llm.TextStartEvent{ContentIndex: 0})
		eventSink.Emit(llm.TextDeltaEvent{ContentIndex: 0, Delta: script.script.response})
		eventSink.Emit(llm.TextEndEvent{ContentIndex: 0, Content: script.script.response})
		eventSink.Done(llm.AssistantMessage{
			Content:    []llm.AssistantContent{llm.TextContent{Text: script.script.response}},
			API:        script.targetModel.API,
			Provider:   script.targetModel.Provider,
			Model:      script.targetModel.ID,
			StopReason: llm.StopReasonStop,
			Timestamp:  time.Now(),
		})
	}), nil
}

func TestClientCreatesAdapterFromTargetModelOnce(t *testing.T) {
	adapterRegistry := llm.NewRegistry()
	constructScript := &constructorScript{api: "shared-protocol", response: "ok"}
	if err := adapterRegistry.Register(constructScript.api, constructScript.construct); err != nil {
		t.Fatal(err)
	}
	targetModel := validModel(constructScript.api, "provider-a")
	targetModel.Headers = map[string]string{"X-Model-Config": "original"}
	llmClient, err := llm.NewClient(targetModel, adapterRegistry, nil)
	if err != nil {
		t.Fatal(err)
	}
	targetModel.Headers["X-Model-Config"] = "mutated"
	if constructScript.createCalls != 1 || constructScript.receivedModel.ID != targetModel.ID {
		t.Fatalf("constructor did not receive target model: calls=%d model=%+v", constructScript.createCalls, constructScript.receivedModel)
	}
	if constructScript.receivedModel.Headers["X-Model-Config"] != "original" {
		t.Fatalf("constructor model changed after client construction: %+v", constructScript.receivedModel.Headers)
	}

	for range 2 {
		assistantReply, err := llmClient.Complete(context.Background(), validContext(), llm.StreamOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if visibleText := llm.Text(assistantReply); visibleText != "ok" {
			t.Fatalf("got response %q", visibleText)
		}
		if assistantReply.Model != targetModel.ID || assistantReply.Provider != targetModel.Provider {
			t.Fatalf("response did not use target model: %+v", assistantReply)
		}
	}
	if constructScript.createCalls != 1 || constructScript.streamCalls != 2 {
		t.Fatalf("create calls=%d stream calls=%d", constructScript.createCalls, constructScript.streamCalls)
	}
}

func TestClientResolvesAPIKeyForTargetProvider(t *testing.T) {
	adapterRegistry := llm.NewRegistry()
	constructScript := &constructorScript{api: "test", response: "ok"}
	if err := adapterRegistry.Register(constructScript.api, constructScript.construct); err != nil {
		t.Fatal(err)
	}
	targetModel := validModel(constructScript.api, "provider-a")
	var resolvedProvider llm.Provider
	keyResolver := llm.APIKeyResolverFunc(func(_ context.Context, servingProvider llm.Provider) (string, bool) {
		resolvedProvider = servingProvider
		return "resolved-key", true
	})
	llmClient, err := llm.NewClient(targetModel, adapterRegistry, keyResolver)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := llmClient.Complete(context.Background(), validContext(), llm.StreamOptions{}); err != nil {
		t.Fatal(err)
	}
	if resolvedProvider != targetModel.Provider {
		t.Fatalf("resolved key for %q, want %q", resolvedProvider, targetModel.Provider)
	}
}

func TestClientReturnsMissingAdapterAtConstruction(t *testing.T) {
	llmClient, err := llm.NewClient(validModel("missing", "provider"), llm.NewRegistry(), nil)
	if llmClient != nil {
		t.Fatal("expected no client")
	}
	if !errors.Is(err, llm.ErrAdapterNotRegistered) {
		t.Fatalf("got error %v", err)
	}
}

func TestRuntimeFailureTerminatesReturnedStream(t *testing.T) {
	adapterRegistry := llm.NewRegistry()
	constructScript := &constructorScript{
		api:     "test",
		failure: errors.New("upstream disconnected"),
	}
	if err := adapterRegistry.Register(constructScript.api, constructScript.construct); err != nil {
		t.Fatal(err)
	}
	llmClient, err := llm.NewClient(validModel(constructScript.api, "provider"), adapterRegistry, nil)
	if err != nil {
		t.Fatal(err)
	}
	responseStream, err := llmClient.Stream(context.Background(), validContext(), llm.StreamOptions{})
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

func TestRegistryUnregistersOnlyMatchingAPI(t *testing.T) {
	adapterRegistry := llm.NewRegistry()
	first := &constructorScript{api: "first"}
	second := &constructorScript{api: "second"}
	if err := adapterRegistry.Register(first.api, first.construct); err != nil {
		t.Fatal(err)
	}
	if err := adapterRegistry.Register(second.api, second.construct); err != nil {
		t.Fatal(err)
	}
	adapterRegistry.Unregister(first.api)
	if _, ok := adapterRegistry.Constructor(first.api); ok {
		t.Fatal("first adapter remains registered")
	}
	if _, ok := adapterRegistry.Constructor(second.api); !ok {
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
