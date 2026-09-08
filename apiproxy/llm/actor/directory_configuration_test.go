package apiactor_test

import (
	"context"
	"testing"
	"time"

	apiactor "github.com/gorenx/goren/apiproxy/llm/actor"
	"github.com/gorenx/goren/llm"
	llmactor "github.com/gorenx/goren/llm/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
)

func TestConfigurationUsesLLMWithoutAgentLoop(t *testing.T) {
	system := startSystem(t)
	ctx := context.Background()
	registry, err := goakt.GrainOf[*llmactor.Grain](ctx, system, "llm", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	source, err := apiactor.NewDirectory(system, registry, registry, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"first", "second"} {
		if err := source.Configure(ctx, "conversation", llm.CallConfig{
			Provider: "p",
			Model:    name,
		}); err != nil {
			t.Fatal(err)
		}
		current, err := source.Configuration(ctx, "conversation")
		if err != nil || current.Model != name {
			t.Fatalf("retained settings: %+v %v", current, err)
		}
		payload, err := system.AskGrain(ctx, registry, llmactor.ReadCall{
			AgentID: "conversation",
		}, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		captured := payload.(llmactor.CallSnapshot)
		if captured.Err != nil || captured.Config.Model != name {
			t.Fatalf("generation settings: %+v", captured)
		}
	}
	if _, err := source.Configuration(ctx, "another"); err == nil {
		t.Fatal("configuration leaked across Agents")
	}
}
