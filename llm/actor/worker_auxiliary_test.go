package llmactor_test

import (
	"context"
	"testing"
	"time"

	"github.com/gorenx/goren/llm"
	llmactor "github.com/gorenx/goren/llm/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
)

func TestAuxiliaryGenerationPreservesAgentConfiguration(t *testing.T) {
	system := startSystem(t, goakt.WithPubSub())
	ctx := context.Background()
	owner, err := goakt.GrainOf[*llmactor.Grain](ctx, system, "llm", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	flow, err := llm.NewSliceStream([]llm.StreamChunk{
		llm.FinishChunk{
			Reason: llm.StopFinish{},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := system.AskGrain(ctx, owner, llmactor.RegisterAdapter{
		Providers: []string{"summary"},
		Adapter: &providerFixture{
			stream: flow,
		},
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if failure := payload.(llmactor.Registered).Err; failure != nil {
		t.Fatal(failure)
	}
	payload, err = system.AskGrain(ctx, owner, llmactor.SetConfiguration{
		AgentID: "a",
		Config: llm.CallConfig{
			Provider: "conversation",
			Model:    "main",
		},
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if failure := payload.(llmactor.ConfigurationResult).Err; failure != nil {
		t.Fatal(failure)
	}
	pool, err := llmactor.SpawnPool(ctx, system, "pool", 1, llmactor.InitializeWorker{
		LLM: owner,
	})
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := goakt.GrainOf[*resultInbox](ctx, system, "results", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan any, 4)
	if _, err := system.AskGrain(ctx, receiver, bindInbox{
		results: results,
	}, time.Second); err != nil {
		t.Fatal(err)
	}
	delivery := llmactor.Delivery{
		CallID:    "summary-call",
		RequestID: "generate",
		ReplyTo:   receiver,
	}
	for _, settings := range []llmactor.Settings{
		nil,
		llmactor.AgentConfig{},
		llmactor.Parameters{},
	} {
		if err := goakt.Tell(ctx, pool, goakt.NewBroadcast(llmactor.Generation{
			Delivery: delivery,
			Settings: settings,
		})); err != nil {
			t.Fatal(err)
		}
		rejected := receiveResult[llmactor.GenerationStarted](t, results)
		if rejected.Err == nil {
			t.Fatalf("invalid settings accepted: %#v", settings)
		}
	}
	// Reusing the identity below also proves rejected settings acquired no Call.
	if err := goakt.Tell(ctx, pool, goakt.NewBroadcast(llmactor.Generation{
		Delivery: delivery,
		Settings: llmactor.Parameters{
			Provider: "summary",
			Model:    "checkpoint",
		},
		Input: llm.Input{
			SessionID: "a",
			Purpose:   llm.PurposeCompaction,
		},
	})); err != nil {
		t.Fatal(err)
	}
	started := receiveResult[llmactor.GenerationStarted](t, results)
	if started.Err != nil || started.Config.Provider != "summary" || started.Config.Model != "checkpoint" {
		t.Fatalf("auxiliary opening: %+v", started)
	}
	delivery.RequestID = "read"
	if err := goakt.Tell(ctx, pool, goakt.NewBroadcast(llmactor.ReadChunk{
		Delivery: delivery,
	})); err != nil {
		t.Fatal(err)
	}
	chunk := receiveResult[llmactor.ChunkRead](t, results)
	if chunk.Err != nil || !chunk.Found {
		t.Fatalf("read: %+v", chunk)
	}
	delivery.RequestID = "close"
	if err := goakt.Tell(ctx, pool, goakt.NewBroadcast(llmactor.CloseCall{
		Delivery: delivery,
	})); err != nil {
		t.Fatal(err)
	}
	if closed := receiveResult[llmactor.CallClosed](t, results); closed.Err != nil {
		t.Fatal(closed.Err)
	}
	payload, err = system.AskGrain(ctx, owner, llmactor.ReadConfiguration{
		AgentID: "a",
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	stored := payload.(llmactor.ConfigurationResult)
	if stored.Err != nil || stored.Config.Provider != "conversation" || stored.Config.Model != "main" {
		t.Fatalf("auxiliary call changed Agent settings: %+v", stored)
	}
}
