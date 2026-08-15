package llm_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
)

type serviceOwnerPlugin struct {
	capture func(llm.LlmRuntime)
}

func (instance *serviceOwnerPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{Name: "test-llm-service", Provides: []plugin.ServiceRef{llm.Service.Ref()}}
}

func (instance *serviceOwnerPlugin) Apply(_ context.Context, pluginScope *plugin.Scope) error {
	serviceValue, err := llm.NewRuntime(pluginScope, nil)
	if err != nil {
		return err
	}
	instance.capture(serviceValue)
	_, err = plugin.Provide(pluginScope, llm.Service, serviceValue)
	return err
}

type adapterOwnerPlugin struct {
	pluginName string
	routes     []string
	backend    llm.Adapter
	capture    func(llm.AdapterRegistrationHandle)
}

func (instance *adapterOwnerPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{Name: instance.pluginName, Requires: []plugin.ServiceRef{llm.Service.Ref()}}
}

func (instance *adapterOwnerPlugin) Apply(requestContext context.Context, pluginScope *plugin.Scope) error {
	serviceValue, found := plugin.Require(pluginScope, llm.Service)
	if !found {
		return errors.New("test: llm service unavailable")
	}
	handleState, err := serviceValue.RegisterAdapter(requestContext, pluginScope, instance.routes, instance.backend)
	if err != nil {
		return err
	}
	if instance.capture != nil {
		instance.capture(handleState)
	}
	return nil
}

type waterfallOwnerPlugin struct {
	pluginName string
	handler    plugin.WaterfallHandler[llm.GenerateOptions, llm.ChunkStream]
}

func (instance *waterfallOwnerPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{Name: instance.pluginName}
}

func (instance *waterfallOwnerPlugin) Apply(_ context.Context, pluginScope *plugin.Scope) error {
	_, err := plugin.OnWaterfall(pluginScope, llm.StreamEvent, instance.handler)
	return err
}

type fakeAdapter struct {
	mu sync.Mutex

	providerName string
	chunks       []llm.StreamChunk
	streamErr    error
	resolved     llm.ResolvedModelInfo
	policy       llm.RetryPolicy
	requests     []llm.GenerateOptions
}

func (backend *fakeAdapter) DescribeProvider(providerRoute string) (llm.ProviderInfo, error) {
	displayName := backend.providerName
	if displayName == "" {
		displayName = providerRoute
	}
	return llm.ProviderInfo{ID: providerRoute, Name: displayName}, nil
}

func (backend *fakeAdapter) ProviderRetryPolicy(string) (llm.RetryPolicy, error) {
	if backend.policy == nil {
		return nil, nil
	}
	return backend.policy.CloneRetryPolicy(), nil
}

func (backend *fakeAdapter) ListModels(_ context.Context, providerRoute string) ([]llm.ModelInfo, error) {
	return []llm.ModelInfo{{Provider: providerRoute, ID: "listed", Name: "Listed"}}, nil
}

func (backend *fakeAdapter) ResolveModel(_ context.Context, providerRoute string, modelID string) (llm.ResolvedModelInfo, error) {
	resolved := backend.resolved
	if resolved.Provider == "" {
		resolved.Provider = providerRoute
	}
	if resolved.ID == "" {
		resolved.ID = modelID
	}
	if resolved.Name == "" {
		resolved.Name = modelID
	}
	return resolved, nil
}

func (backend *fakeAdapter) Stream(_ context.Context, options llm.GenerateOptions) (llm.ChunkStream, error) {
	if backend.streamErr != nil {
		return nil, backend.streamErr
	}
	backend.mu.Lock()
	backend.requests = append(backend.requests, options)
	backend.mu.Unlock()
	return llm.NewSliceStream(backend.chunks)
}

func (backend *fakeAdapter) requestSnapshots() []llm.GenerateOptions {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	return append([]llm.GenerateOptions(nil), backend.requests...)
}

func bootstrapRuntime(t *testing.T) (*plugin.Runtime, llm.LlmRuntime) {
	t.Helper()
	engine := plugin.NewRuntime()
	var serviceValue llm.LlmRuntime
	instance := &serviceOwnerPlugin{capture: func(available llm.LlmRuntime) {
		serviceValue = available
	}}
	if _, err := engine.Load(context.Background(), instance); err != nil {
		t.Fatal(err)
	}
	if serviceValue == nil {
		t.Fatal("llm service was not captured")
	}
	return engine, serviceValue
}

func loadAdapter(
	t *testing.T,
	engine *plugin.Runtime,
	pluginName string,
	routes []string,
	backend llm.Adapter,
) (plugin.Handle, llm.AdapterRegistrationHandle) {
	t.Helper()
	var handleState llm.AdapterRegistrationHandle
	instance := &adapterOwnerPlugin{
		pluginName: pluginName, routes: routes, backend: backend,
		capture: func(available llm.AdapterRegistrationHandle) { handleState = available },
	}
	pluginHandle, err := engine.Load(context.Background(), instance)
	if err != nil {
		t.Fatal(err)
	}
	return pluginHandle, handleState
}

func drainChunks(t *testing.T, flow llm.ChunkStream) []llm.StreamChunk {
	t.Helper()
	entries := []llm.StreamChunk{}
	for {
		entry, present, err := flow.Next(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !present {
			break
		}
		entries = append(entries, entry)
	}
	return entries
}

func TestRuntimeRoutesPreparedCallAndCapturesAdapterDefaults(t *testing.T) {
	t.Parallel()
	engine, serviceValue := bootstrapRuntime(t)
	defaultMaxTokens := 128
	backend := &fakeAdapter{
		providerName: "Example",
		resolved: llm.ResolvedModelInfo{
			DefaultMaxTokens: &defaultMaxTokens,
			Context:          &llm.ModelContext{ContextWindow: 4096},
			Reasoning: &llm.ModelReasoningInfo{
				Efforts:       []llm.ReasoningEffortInfo{{ID: "low", Name: "Low"}, {ID: "high", Name: "High"}},
				DefaultEffort: "high",
			},
		},
		chunks: []llm.StreamChunk{llm.TextDeltaChunk{Index: 0, Text: "ok"}, llm.FinishChunk{Reason: llm.StopFinish{}}},
	}
	loadAdapter(t, engine, "test-adapter", []string{"example"}, backend)
	if got := serviceValue.ListProviders(); !reflect.DeepEqual(got, []llm.ProviderInfo{{ID: "example", Name: "Example"}}) {
		t.Fatalf("providers = %#v", got)
	}
	prepared, err := serviceValue.PrepareCall(context.Background(), llm.CallConfig{Provider: "example", Model: "unlisted"})
	if err != nil {
		t.Fatal(err)
	}
	resolved := prepared.ConfigValue()
	if resolved.MaxTokens == nil || *resolved.MaxTokens != 128 || resolved.ReasoningEffort != "high" {
		t.Fatalf("prepared config = %#v", resolved)
	}
	modelContext, found := prepared.ContextValue()
	if !found || modelContext.ContextWindow != 4096 {
		t.Fatalf("prepared context = (%#v, %t)", modelContext, found)
	}
	if defaults := prepared.AdapterDefaultsValue(); !defaults.MaxTokens || !defaults.ReasoningEffort {
		t.Fatalf("adapter defaults = %#v", defaults)
	}
	flow, err := prepared.Stream(context.Background(), llm.GenerateOptions{CallConfig: resolved})
	if err != nil {
		t.Fatal(err)
	}
	entries := drainChunks(t, flow)
	if len(entries) != 2 || entries[0].ChunkType() != "text-delta" || entries[1].ChunkType() != "finish" {
		t.Fatalf("stream entries = %#v", entries)
	}
	if _, err := prepared.Stream(context.Background(), llm.GenerateOptions{CallConfig: resolved}); llmErrorCode(err) != "INVALID_PREPARED_CALL" {
		t.Fatalf("second prepared stream error = %v", err)
	}
	if got := backend.requestSnapshots(); len(got) != 1 || got[0].Model != "unlisted" {
		t.Fatalf("adapter requests = %#v", got)
	}
}

func TestRuntimeNormalizesFinalAdapterFailures(t *testing.T) {
	t.Parallel()
	engine, serviceValue := bootstrapRuntime(t)
	backend := &fakeAdapter{streamErr: llm.MustLlmError("busy", "SERVER", llm.LlmErrorOptions{Status: intPointer(503)})}
	loadAdapter(t, engine, "failing-adapter", []string{"failing"}, backend)
	flow, err := serviceValue.Stream(context.Background(), llm.GenerateOptions{CallConfig: llm.CallConfig{Provider: "failing", Model: "m"}})
	if err != nil {
		t.Fatal(err)
	}
	entries := drainChunks(t, flow)
	if len(entries) != 1 {
		t.Fatalf("failure entries = %#v", entries)
	}
	terminal := entries[0].(llm.FinishChunk)
	problem := terminal.Reason.(llm.ErrorFinish)
	if problem.Failure.Code != "SERVER" || problem.Failure.Status == nil || *problem.Failure.Status != 503 {
		t.Fatalf("terminal failure = %#v", problem.Failure)
	}
	missingFlow, err := serviceValue.Stream(context.Background(), llm.GenerateOptions{CallConfig: llm.CallConfig{Provider: "missing", Model: "m"}})
	if err != nil {
		t.Fatal(err)
	}
	missing := drainChunks(t, missingFlow)[0].(llm.FinishChunk).Reason.(llm.ErrorFinish)
	if missing.Failure.Code != "NO_ADAPTER" {
		t.Fatalf("missing provider failure = %#v", missing.Failure)
	}
}

func TestRuntimeLeavesWaterfallFailureOutsideAdapterNormalization(t *testing.T) {
	t.Parallel()
	engine, serviceValue := bootstrapRuntime(t)
	backend := &fakeAdapter{chunks: []llm.StreamChunk{llm.FinishChunk{Reason: llm.StopFinish{}}}}
	loadAdapter(t, engine, "healthy-adapter", []string{"healthy"}, backend)
	middlewareErr := errors.New("middleware failed")
	listener := &waterfallOwnerPlugin{
		pluginName: "failing-waterfall",
		handler: func(context.Context, llm.GenerateOptions, plugin.Next[llm.GenerateOptions, llm.ChunkStream]) (llm.ChunkStream, error) {
			return nil, middlewareErr
		},
	}
	if _, err := engine.Load(context.Background(), listener); err != nil {
		t.Fatal(err)
	}
	_, err := serviceValue.Stream(context.Background(), llm.GenerateOptions{CallConfig: llm.CallConfig{Provider: "healthy", Model: "m"}})
	if !errors.Is(err, middlewareErr) {
		t.Fatalf("waterfall error = %v", err)
	}
}

func TestAdapterRouteReplacementIsAtomicAndEffectOwned(t *testing.T) {
	t.Parallel()
	engine, serviceValue := bootstrapRuntime(t)
	first := &fakeAdapter{}
	firstPlugin, firstHandle := loadAdapter(t, engine, "first-adapter", []string{"first"}, first)
	second := &fakeAdapter{}
	loadAdapter(t, engine, "second-adapter", []string{"occupied"}, second)
	if err := firstHandle.Replace(context.Background(), []string{"occupied"}); llmErrorCode(err) != "DUPLICATE_ADAPTER" {
		t.Fatalf("conflicting replace error = %v", err)
	}
	if got := serviceValue.ListProviders(); !reflect.DeepEqual(got, []llm.ProviderInfo{
		{ID: "first", Name: "first"}, {ID: "occupied", Name: "occupied"},
	}) {
		t.Fatalf("providers after rejected replace = %#v", got)
	}
	if err := engine.Unload(context.Background(), firstPlugin); err != nil {
		t.Fatal(err)
	}
	if got := serviceValue.ListProviders(); !reflect.DeepEqual(got, []llm.ProviderInfo{{ID: "occupied", Name: "occupied"}}) {
		t.Fatalf("providers after owner unload = %#v", got)
	}
	if err := firstHandle.Replace(context.Background(), []string{"late"}); llmErrorCode(err) != "REGISTRATION_DISPOSED" {
		t.Fatalf("replace after disposal error = %v", err)
	}
}

func TestRuntimeFiltersReplayStateByAdapterInstance(t *testing.T) {
	t.Parallel()
	engine, serviceValue := bootstrapRuntime(t)
	sameBackend := &fakeAdapter{chunks: []llm.StreamChunk{llm.FinishChunk{Reason: llm.StopFinish{}}}}
	otherBackend := &fakeAdapter{chunks: []llm.StreamChunk{llm.FinishChunk{Reason: llm.StopFinish{}}}}
	loadAdapter(t, engine, "same-adapter", []string{"historical", "same-target"}, sameBackend)
	loadAdapter(t, engine, "other-adapter", []string{"other-target"}, otherBackend)
	assistantEntry, err := llm.NewAssistantMessage(llm.AssistantMessageInput{
		Content: []llm.ContentBlock{llm.NewTextBlock("answer")},
		Source: llm.ModelMessageSource{
			Provider: "historical", Model: "m", ReplayState: json.RawMessage(`{"opaque":true}`),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, providerRoute := range []string{"same-target", "other-target"} {
		flow, streamErr := serviceValue.Stream(context.Background(), llm.GenerateOptions{
			CallConfig: llm.CallConfig{Provider: providerRoute, Model: "m"}, Messages: []llm.Message{assistantEntry},
		})
		if streamErr != nil {
			t.Fatal(streamErr)
		}
		drainChunks(t, flow)
	}
	sameOrigin := sameBackend.requestSnapshots()[0].Messages[0].SourceValue().(llm.ModelMessageSource)
	otherOrigin := otherBackend.requestSnapshots()[0].Messages[0].SourceValue().(llm.ModelMessageSource)
	if len(sameOrigin.ReplayState) == 0 || len(otherOrigin.ReplayState) != 0 {
		t.Fatalf("replay states = (same:%s other:%s)", sameOrigin.ReplayState, otherOrigin.ReplayState)
	}
}

func TestWaterfallCanRouteBeforeFinalAdapterSelection(t *testing.T) {
	t.Parallel()
	engine, serviceValue := bootstrapRuntime(t)
	backend := &fakeAdapter{chunks: []llm.StreamChunk{llm.FinishChunk{Reason: llm.StopFinish{}}}}
	loadAdapter(t, engine, "routed-adapter", []string{"routed"}, backend)
	listener := &waterfallOwnerPlugin{
		pluginName: "routing-waterfall",
		handler: func(requestContext context.Context, options llm.GenerateOptions, delegate plugin.Next[llm.GenerateOptions, llm.ChunkStream]) (llm.ChunkStream, error) {
			options.Provider = "routed"
			return delegate(requestContext, options)
		},
	}
	if _, err := engine.Load(context.Background(), listener); err != nil {
		t.Fatal(err)
	}
	flow, err := serviceValue.Stream(context.Background(), llm.GenerateOptions{CallConfig: llm.CallConfig{Provider: "unrouted", Model: "m"}})
	if err != nil {
		t.Fatal(err)
	}
	drainChunks(t, flow)
	if got := backend.requestSnapshots(); len(got) != 1 || got[0].Provider != "routed" {
		t.Fatalf("routed requests = %#v", got)
	}
}

func llmErrorCode(problem error) string {
	var typedError *llm.LlmError
	if errors.As(problem, &typedError) {
		return typedError.Code()
	}
	return ""
}

func intPointer(value int) *int { return &value }
