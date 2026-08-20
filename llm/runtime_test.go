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

type adapterOwnerPlugin struct {
	plugin.Base
	pluginName   string
	routes       []string
	backend      llm.Adapter
	registration llm.AdapterRegistrationHandle
}

func (instance *adapterOwnerPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: instance.pluginName,
		Requires: []plugin.ServiceType{
			plugin.ServiceOf[llm.LlmRuntime](),
		},
	}
}

func (instance *adapterOwnerPlugin) Apply(requestContext context.Context) error {
	serviceValue, err := plugin.Require[llm.LlmRuntime](instance)
	if err != nil {
		return err
	}
	handleState, err := serviceValue.RegisterAdapter(requestContext, instance.routes, instance.backend)
	if err != nil {
		return err
	}
	instance.registration = handleState
	return nil
}

func (instance *adapterOwnerPlugin) Dispose(closeContext context.Context) error {
	if instance.registration == nil {
		return nil
	}
	return instance.registration.Release(closeContext)
}

type waterfallOwnerPlugin struct {
	plugin.Base
	pluginName string
	failure    error
	route      string
}

func (instance *waterfallOwnerPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: instance.pluginName,
		Waterfalls: []plugin.WaterfallContribution{
			plugin.WaterfallOf[llm.GenerateOptions, llm.StreamOutput](instance),
		},
	}
}

func (*waterfallOwnerPlugin) Apply(requestContext context.Context) error {
	return requestContext.Err()
}

func (*waterfallOwnerPlugin) Dispose(context.Context) error {
	return nil
}

func (instance *waterfallOwnerPlugin) Intercept(
	requestContext context.Context,
	options llm.GenerateOptions,
	next plugin.WaterfallAction[llm.GenerateOptions, llm.StreamOutput],
) (llm.StreamOutput, error) {
	if instance.failure != nil {
		return llm.StreamOutput{}, instance.failure
	}
	if instance.route != "" {
		options.Provider = instance.route
	}
	return next.Execute(requestContext, options)
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
	return llm.ProviderInfo{
		ID:   providerRoute,
		Name: displayName,
	}, nil
}

func (backend *fakeAdapter) ProviderRetryPolicy(string) (llm.RetryPolicy, error) {
	if backend.policy == nil {
		return nil, nil
	}
	return backend.policy.CloneRetryPolicy(), nil
}

func (backend *fakeAdapter) ListModels(_ context.Context, providerRoute string) ([]llm.ModelInfo, error) {
	return []llm.ModelInfo{
		{
			Provider: providerRoute,
			ID:       "listed",
			Name:     "Listed",
		},
	}, nil
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
	engine := plugin.NewRuntime(plugin.RuntimeSettings{})
	serviceValue := llm.NewRuntime(nil)
	if _, err := engine.Start(context.Background(), serviceValue); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := engine.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown LLM Runtime: %v", err)
		}
	})
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
	instance := &adapterOwnerPlugin{
		pluginName: pluginName,
		routes:     routes,
		backend:    backend,
	}
	pluginHandle, err := engine.Mount(context.Background(), instance)
	if err != nil {
		t.Fatal(err)
	}
	return pluginHandle, instance.registration
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
			Context: &llm.ModelContext{
				ContextWindow: 4096,
			},
			Reasoning: &llm.ModelReasoningInfo{
				Efforts: []llm.ReasoningEffortInfo{
					{
						ID:   "low",
						Name: "Low",
					},
					{
						ID:   "high",
						Name: "High",
					},
				},
				DefaultEffort: "high",
			},
		},
		chunks: []llm.StreamChunk{
			llm.TextDeltaChunk{
				Index: 0,
				Text:  "ok",
			},
			llm.FinishChunk{
				Reason: llm.StopFinish{},
			},
		},
	}
	loadAdapter(t, engine, "test-adapter", []string{"example"}, backend)
	if got := serviceValue.ListProviders(); !reflect.DeepEqual(got, []llm.ProviderInfo{
		{
			ID:   "example",
			Name: "Example",
		},
	}) {
		t.Fatalf("providers = %#v", got)
	}
	prepared, err := serviceValue.PrepareCall(context.Background(), llm.CallConfig{
		Provider: "example",
		Model:    "unlisted",
	})
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
	flow, err := prepared.Stream(context.Background(), llm.GenerateOptions{
		CallConfig: resolved,
	})
	if err != nil {
		t.Fatal(err)
	}
	entries := drainChunks(t, flow)
	if len(entries) != 2 || entries[0].ChunkType() != "text-delta" || entries[1].ChunkType() != "finish" {
		t.Fatalf("stream entries = %#v", entries)
	}
	if _, err := prepared.Stream(context.Background(), llm.GenerateOptions{
		CallConfig: resolved,
	}); llmErrorCode(err) != "INVALID_PREPARED_CALL" {
		t.Fatalf("second prepared stream error = %v", err)
	}
	if got := backend.requestSnapshots(); len(got) != 1 || got[0].Model != "unlisted" {
		t.Fatalf("adapter requests = %#v", got)
	}
}

func TestRuntimeNormalizesFinalAdapterFailures(t *testing.T) {
	t.Parallel()
	engine, serviceValue := bootstrapRuntime(t)
	backend := &fakeAdapter{
		streamErr: llm.MustLlmError("busy", "SERVER", llm.LlmErrorOptions{
			Status: intPointer(503),
		}),
	}
	loadAdapter(t, engine, "failing-adapter", []string{"failing"}, backend)
	flow, err := serviceValue.Stream(context.Background(), llm.GenerateOptions{
		CallConfig: llm.CallConfig{
			Provider: "failing",
			Model:    "m",
		},
	})
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
	missingFlow, err := serviceValue.Stream(context.Background(), llm.GenerateOptions{
		CallConfig: llm.CallConfig{
			Provider: "missing",
			Model:    "m",
		},
	})
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
	backend := &fakeAdapter{
		chunks: []llm.StreamChunk{
			llm.FinishChunk{
				Reason: llm.StopFinish{},
			},
		},
	}
	loadAdapter(t, engine, "healthy-adapter", []string{"healthy"}, backend)
	middlewareErr := errors.New("middleware failed")
	listener := &waterfallOwnerPlugin{
		pluginName: "failing-waterfall",
		failure:    middlewareErr,
	}
	if _, err := engine.Mount(context.Background(), listener); err != nil {
		t.Fatal(err)
	}
	_, err := serviceValue.Stream(context.Background(), llm.GenerateOptions{
		CallConfig: llm.CallConfig{
			Provider: "healthy",
			Model:    "m",
		},
	})
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
		{
			ID:   "first",
			Name: "first",
		},
		{
			ID:   "occupied",
			Name: "occupied",
		},
	}) {
		t.Fatalf("providers after rejected replace = %#v", got)
	}
	if err := engine.Unload(context.Background(), firstPlugin); err != nil {
		t.Fatal(err)
	}
	if got := serviceValue.ListProviders(); !reflect.DeepEqual(got, []llm.ProviderInfo{
		{
			ID:   "occupied",
			Name: "occupied",
		},
	}) {
		t.Fatalf("providers after owner unload = %#v", got)
	}
	if err := firstHandle.Replace(context.Background(), []string{"late"}); llmErrorCode(err) != "REGISTRATION_DISPOSED" {
		t.Fatalf("replace after disposal error = %v", err)
	}
}

func TestRuntimeFiltersReplayStateByAdapterInstance(t *testing.T) {
	t.Parallel()
	engine, serviceValue := bootstrapRuntime(t)
	sameBackend := &fakeAdapter{
		chunks: []llm.StreamChunk{
			llm.FinishChunk{
				Reason: llm.StopFinish{},
			},
		},
	}
	otherBackend := &fakeAdapter{
		chunks: []llm.StreamChunk{
			llm.FinishChunk{
				Reason: llm.StopFinish{},
			},
		},
	}
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
			CallConfig: llm.CallConfig{
				Provider: providerRoute,
				Model:    "m",
			},
			Messages: []llm.Message{assistantEntry},
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
	backend := &fakeAdapter{
		chunks: []llm.StreamChunk{
			llm.FinishChunk{
				Reason: llm.StopFinish{},
			},
		},
	}
	loadAdapter(t, engine, "routed-adapter", []string{"routed"}, backend)
	listener := &waterfallOwnerPlugin{
		pluginName: "routing-waterfall",
		route:      "routed",
	}
	if _, err := engine.Mount(context.Background(), listener); err != nil {
		t.Fatal(err)
	}
	flow, err := serviceValue.Stream(context.Background(), llm.GenerateOptions{
		CallConfig: llm.CallConfig{
			Provider: "unrouted",
			Model:    "m",
		},
	})
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
