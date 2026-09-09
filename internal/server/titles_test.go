package server

import (
	"context"
	"testing"
	"time"

	"github.com/gorenx/goren/session/title"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
)

func TestTitleConfigurationRejectsMissingProviderBeforeLifecycleBinding(t *testing.T) {
	ctx := context.Background()
	system, err := goakt.NewActorSystem("title-configuration", goakt.WithPubSub(), goakt.WithLogger(log.DiscardLogger))
	if err != nil {
		t.Fatal(err)
	}
	err = system.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		failure := system.Stop(context.Background())
		if failure != nil {
			t.Error(failure)
		}
	})
	settings := title.Config{
		FallbackMaxWords: 5,
		FallbackMaxBytes: 40,
		MaxTitleBytes:    80,
		LLM: &title.LLMConfig{
			AutomaticMode:       title.AutomaticFirstPrompt,
			TargetWords:         5,
			TargetCJKCharacters: 10,
			MaxInputBytes:       1024,
			MaxOutputTokens:     64,
			TimeoutMS:           1000,
			Provider:            "deepseek",
			Model:               "deepseek-chat",
		},
	}
	endpoint, err := initializeTitles(ctx, system, settings, nil, time.Second)
	if err == nil || endpoint != nil {
		t.Fatalf("missing provider accepted: %v %v", endpoint, err)
	}
	invalid := settings
	invalid.FallbackMaxWords = 0
	endpoint, err = initializeTitles(ctx, system, invalid, nil, time.Second)
	if err == nil || endpoint != nil {
		t.Fatalf("invalid policy accepted: %v %v", endpoint, err)
	}
	// Rejection must not consume the one-time Registry configuration slot.
	settings.LLM = nil
	endpoint, err = initializeTitles(ctx, system, settings, nil, time.Second)
	if err != nil || endpoint == nil {
		t.Fatalf("corrected configuration failed: %v %v", endpoint, err)
	}
}
