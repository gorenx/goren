package modelquery_test

import (
	"context"
	"errors"
	"github.com/gorenx/goren/apiproxy/modelquery"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorenx/goren/llm"
	llmactor "github.com/gorenx/goren/llm/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

type metadataProvider struct {
	generations atomic.Int32
	slow        chan struct{}
	exited      chan struct{}
}

func (backend *metadataProvider) Stream(context.Context, llm.GenerateOptions) (llm.ChunkStream, error) {
	backend.generations.Add(1)
	return nil, errors.New("metadata query must not generate")
}

func (*metadataProvider) ListModels(context.Context, string) ([]llm.ModelInfo, error) {
	return []llm.ModelInfo{{
		Provider: "p",
		ID:       "m",
		Name:     "Model",
	}}, nil
}

func (backend *metadataProvider) ResolveModel(ctx context.Context, provider string, name string) (llm.ResolvedModelInfo, error) {
	if name == "slow" {
		close(backend.slow)
		<-ctx.Done()
		if backend.exited != nil {
			close(backend.exited)
		}
		return llm.ResolvedModelInfo{}, ctx.Err()
	}
	limit := 128
	return llm.ResolvedModelInfo{
		ModelInfo: llm.ModelInfo{
			Provider: provider,
			ID:       name,
			Name:     "Model",
		},
		DefaultMaxTokens: &limit,
	}, nil
}

func TestRouterMetadataQueriesDoNotBlockWorkerOrGenerate(t *testing.T) {
	system := startSystem(t, goakt.WithPubSub())
	ctx := context.Background()
	registry, err := goakt.GrainOf[*llmactor.Grain](ctx, system, "registry", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := goakt.GrainOf[*resultInbox](ctx, system, "results", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan any, 8)
	if _, err := system.AskGrain(ctx, receiver, bindInbox{results: results}, time.Second); err != nil {
		t.Fatal(err)
	}
	backend := &metadataProvider{
		slow: make(chan struct{}),
	}
	payload, err := system.AskGrain(ctx, registry, llmactor.RegisterAdapter{
		Providers: []string{"p"},
		Adapter:   backend,
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if failure := payload.(llmactor.Registered).Err; failure != nil {
		t.Fatal(failure)
	}
	pool, err := llmactor.SpawnPool(ctx, system, "pool", 1, llmactor.InitializeWorker{
		LLM: registry,
	})
	if err != nil {
		t.Fatal(err)
	}
	send := func(message any) {
		t.Helper()
		if err := goakt.Tell(ctx, pool, goakt.NewBroadcast(message)); err != nil {
			t.Fatal(err)
		}
	}
	route := llmactor.Query{
		RequestID: "slow",
		ReplyTo:   receiver,
		Deadline:  time.Now().Add(2 * time.Second),
	}
	send(llmactor.ResolveModel{
		Query:    route,
		Provider: "p",
		Model:    "slow",
	})
	select {
	case <-backend.slow:
	case <-time.After(time.Second):
		t.Fatal("slow metadata query did not start")
	}
	route.RequestID = "catalog"
	route.Deadline = time.Now().Add(time.Second)
	send(llmactor.ListModels{
		Query:    route,
		Provider: "p",
	})
	listed := receiveResult[llmactor.ModelsListed](t, results)
	if listed.Err != nil || listed.RequestID != "catalog" || len(listed.Models) != 1 {
		t.Fatalf("catalog: %+v", listed)
	}
	route.RequestID = "config"
	send(llmactor.ResolveConfig{
		Query: route,
		Config: llm.CallConfig{
			Provider: "p",
			Model:    "m",
		},
	})
	resolved := receiveResult[llmactor.ConfigResolved](t, results)
	if resolved.Err != nil || resolved.Config.MaxTokens == nil || *resolved.Config.MaxTokens != 128 {
		t.Fatalf("config: %+v", resolved)
	}
	slow := receiveResult[llmactor.ModelResolved](t, results)
	if slow.RequestID != "slow" || !errors.Is(slow.Err, context.DeadlineExceeded) {
		t.Fatalf("deadline: %+v", slow)
	}
	if backend.generations.Load() != 0 {
		t.Fatal("metadata query dispatched generation")
	}
}

func TestQueryCallerCancellationReachesProviderThroughRouter(t *testing.T) {
	system := startSystem(t, goakt.WithPubSub())
	ctx := context.Background()
	registry, err := goakt.GrainOf[*llmactor.Grain](ctx, system, "registry", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	backend := &metadataProvider{
		slow:   make(chan struct{}),
		exited: make(chan struct{}),
	}
	payload, err := system.AskGrain(ctx, registry, llmactor.RegisterAdapter{
		Providers: []string{"p"},
		Adapter:   backend,
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if failure := payload.(llmactor.Registered).Err; failure != nil {
		t.Fatal(failure)
	}
	pool, err := llmactor.SpawnPool(ctx, system, "pool", 1, llmactor.InitializeWorker{
		LLM: registry,
	})
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := goakt.GrainOf[*modelquery.Grain](ctx, system, "bridge", goakt.WithLongLivedGrain(), goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		t.Fatal(err)
	}
	payload, err = system.AskGrain(ctx, bridge, modelquery.Initialize{
		Router: pool,
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if failure := payload.(llmactor.Completion).Err; failure != nil {
		t.Fatal(failure)
	}
	caller, cancel := context.WithCancel(ctx)
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		_, err := system.AskGrain(caller, bridge, llmactor.ResolveModel{
			Query: llmactor.Query{
				RequestID: "cancelled",
				Deadline:  time.Now().Add(10 * time.Second),
			},
			Provider: "p",
			Model:    "slow",
		}, 10*time.Second)
		finished <- err
	}()
	select {
	case <-backend.slow:
	case <-time.After(time.Second):
		t.Fatal("provider query did not start")
	}
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("caller result: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("caller did not stop waiting")
	}
	if err := system.TellGrain(ctx, bridge, llmactor.CancelQuery{
		RequestID: "cancelled",
		Cause:     context.Canceled,
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-backend.exited:
	case <-time.After(time.Second):
		t.Fatal("cancellation stopped only the caller, not provider I/O")
	}
}
