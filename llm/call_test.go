package llm_test

import (
	"context"
	"errors"
	"github.com/gorenx/goren/llm"
	"reflect"
	"sync"
	"testing"
)

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

func TestCallCapturesAdapterDefaults(t *testing.T) {
	t.Parallel()
	var catalog llm.Registry
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
	if _, err := catalog.RegisterAdapter([]string{"example"}, backend); err != nil {
		t.Fatal(err)
	}
	if got := catalog.ListProviders(); !reflect.DeepEqual(got, []llm.ProviderInfo{
		{
			ID:   "example",
			Name: "Example",
		},
	}) {
		t.Fatalf("providers = %#v", got)
	}
	selected, err := catalog.Select("example")
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := llm.NewCall(context.Background(), selected, llm.CallConfig{
		Provider: "example",
		Model:    "unlisted",
	}, nil)
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
	err = prepared.Open(context.Background(), llm.GenerateOptions{
		CallConfig: resolved,
	})
	if err != nil {
		t.Fatal(err)
	}
	entries := drainChunks(t, prepared)
	if len(entries) != 2 || entries[0].ChunkType() != "text-delta" || entries[1].ChunkType() != "finish" {
		t.Fatalf("stream entries = %#v", entries)
	}
	if err := prepared.Open(context.Background(), llm.GenerateOptions{
		CallConfig: resolved,
	}); llmErrorCode(err) != "INVALID_PREPARED_CALL" {
		t.Fatalf("second prepared stream error = %v", err)
	}
	if got := backend.requestSnapshots(); len(got) != 1 || got[0].Model != "unlisted" {
		t.Fatalf("adapter requests = %#v", got)
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

func TestBindingSurvivesReplacementAndRejectsOtherProvider(t *testing.T) {
	var catalog llm.Registry
	original := &fakeAdapter{}
	id, err := catalog.RegisterAdapter([]string{"p"}, original)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := catalog.Select("p")
	if err != nil {
		t.Fatal(err)
	}
	if !catalog.RemoveAdapter(id) {
		t.Fatal("registration was not removed")
	}
	replacement := &fakeAdapter{}
	if _, err := catalog.RegisterAdapter([]string{"p"}, replacement); err != nil {
		t.Fatal(err)
	}
	config := llm.CallConfig{
		Provider: "p",
		Model:    "m",
	}
	prepared, err := llm.NewCall(context.Background(), selected, config, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = prepared.Open(context.Background(), llm.GenerateOptions{
		CallConfig: config,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := prepared.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(original.requestSnapshots()) != 1 || len(replacement.requestSnapshots()) != 0 {
		t.Fatal("prepared binding followed replacement")
	}
	config.Provider = "other"
	if _, err := selected.ResolveCallConfig(context.Background(), config); llmErrorCode(err) != "INVALID_ARGS" {
		t.Fatalf("wrong provider: %v", err)
	}
}

type replacementStream struct {
	failure error
	stream  llm.ChunkStream
}

func (handler replacementStream) Intercept(context.Context, llm.GenerateOptions, llm.Streamer) (llm.ChunkStream, error) {
	return handler.stream, handler.failure
}

func TestCallSupportsReplacementWithoutAdapterAndPreservesMiddlewareError(t *testing.T) {
	failed := errors.New("middleware failed")
	for _, failure := range []error{nil, failed} {
		stream, err := llm.NewSliceStream([]llm.StreamChunk{llm.FinishChunk{
			Reason: llm.StopFinish{},
		}})
		if err != nil {
			t.Fatal(err)
		}
		prepared, err := llm.NewCall(context.Background(), nil, llm.CallConfig{
			Provider: "missing",
			Model:    "m",
		}, []llm.StreamMiddleware{replacementStream{
			failure: failure,
			stream:  stream,
		}})
		if err != nil || prepared == nil {
			t.Fatalf("missing adapter: %v", err)
		}
		err = prepared.Open(context.Background(), llm.GenerateOptions{
			CallConfig: prepared.ConfigValue(),
		})
		if !errors.Is(err, failure) {
			t.Fatalf("replacement lost stream or error: %v", err)
		}
		if err := prepared.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCallNormalizesProviderFailureAndStopsAtFirstFinish(t *testing.T) {
	for _, failure := range []error{nil, llm.MustLlmError("limited", "RATE_LIMITED")} {
		var catalog llm.Registry
		backend := &fakeAdapter{
			streamErr: failure,
			chunks: []llm.StreamChunk{
				llm.FinishChunk{Reason: llm.StopFinish{}},
				llm.TextDeltaChunk{Text: "must not appear"},
			},
		}
		if _, err := catalog.RegisterAdapter([]string{"p"}, backend); err != nil {
			t.Fatal(err)
		}
		selected, err := catalog.Select("p")
		if err != nil {
			t.Fatal(err)
		}
		prepared, err := llm.NewCall(context.Background(), selected, llm.CallConfig{
			Provider: "p",
			Model:    "m",
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		err = prepared.Open(context.Background(), llm.GenerateOptions{
			CallConfig: prepared.ConfigValue(),
		})
		if err != nil {
			t.Fatal(err)
		}
		entries := drainChunks(t, prepared)
		if len(entries) != 1 || entries[0].ChunkType() != "finish" {
			t.Fatalf("terminal boundary: %#v", entries)
		}
		if failure != nil {
			if _, ok := entries[0].(llm.FinishChunk).Reason.(llm.ErrorFinish); !ok {
				t.Fatal("provider failure not normalized")
			}
		}
		if err := prepared.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}

type resourceAdapter struct {
	stream  llm.ChunkStream
	failure error
	calls   int
}

func (backend *resourceAdapter) Stream(context.Context, llm.GenerateOptions) (llm.ChunkStream, error) {
	backend.calls++
	return backend.stream, backend.failure
}

type retainedStream struct {
	failure error
	closes  int
}

func (*retainedStream) Next(context.Context) (llm.StreamChunk, bool, error) { return nil, false, nil }
func (flow *retainedStream) Close(context.Context) error {
	flow.closes++
	if flow.closes == 1 {
		return flow.failure
	}
	return nil
}

func TestCallRetainsPartialResourcesUntilCloseSucceeds(t *testing.T) {
	failed := errors.New("close failed")
	stream := &retainedStream{
		failure: failed,
	}
	backend := &resourceAdapter{
		stream:  stream,
		failure: errors.New("partially opened"),
	}
	var catalog llm.Registry
	if _, err := catalog.RegisterAdapter([]string{"p"}, backend); err != nil {
		t.Fatal(err)
	}
	selected, err := catalog.Select("p")
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := llm.NewCall(context.Background(), selected, llm.CallConfig{
		Provider: "p",
		Model:    "m",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	options := llm.GenerateOptions{
		CallConfig: prepared.ConfigValue(),
	}
	if err := prepared.Open(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	if err := prepared.Open(context.Background(), options); llmErrorCode(err) != "INVALID_PREPARED_CALL" {
		t.Fatalf("duplicate open: %v", err)
	}
	if backend.calls != 1 {
		t.Fatal("duplicate provider dispatch")
	}
	if err := prepared.Close(context.Background()); !errors.Is(err, failed) {
		t.Fatalf("first close: %v", err)
	}
	if err := prepared.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := prepared.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if stream.closes != 2 {
		t.Fatalf("close attempts: %d", stream.closes)
	}
	if err := prepared.Open(context.Background(), options); llmErrorCode(err) != "INVALID_PREPARED_CALL" {
		t.Fatalf("open after closure: %v", err)
	}
	if _, _, err := prepared.Next(context.Background()); err == nil {
		t.Fatal("read after closure succeeded")
	}
}

type failingWrapper struct{ failure error }

func (handler failingWrapper) Intercept(ctx context.Context, options llm.GenerateOptions, next llm.Streamer) (llm.ChunkStream, error) {
	if _, err := next.Stream(ctx, options); err != nil {
		return nil, err
	}
	return nil, handler.failure
}

func TestCallClosesTerminalWhenMiddlewareDiscardsIt(t *testing.T) {
	stream := &retainedStream{}
	var catalog llm.Registry
	if _, err := catalog.RegisterAdapter([]string{"p"}, &resourceAdapter{stream: stream}); err != nil {
		t.Fatal(err)
	}
	selected, err := catalog.Select("p")
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("wrapper construction failed")
	prepared, err := llm.NewCall(context.Background(), selected, llm.CallConfig{
		Provider: "p",
		Model:    "m",
	}, []llm.StreamMiddleware{failingWrapper{failure: failure}})
	if err != nil {
		t.Fatal(err)
	}
	if err := prepared.Open(context.Background(), llm.GenerateOptions{CallConfig: prepared.ConfigValue()}); !errors.Is(err, failure) {
		t.Fatalf("open: %v", err)
	}
	if err := prepared.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if stream.closes != 1 {
		t.Fatalf("terminal resource closed %d times", stream.closes)
	}
}
