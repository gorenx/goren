package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	credentialslocal "github.com/gorenx/goren/credentials/local"
	"github.com/gorenx/goren/llm"
	llmactor "github.com/gorenx/goren/llm/actor"
	"github.com/gorenx/goren/llm/deepseek"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
)

type providerEnvironment struct {
	home string
}

func (environment providerEnvironment) Lookup(string) (string, bool) { return "", false }
func (environment providerEnvironment) UserHomeDir() (string, error) { return environment.home, nil }

type modelReplies struct {
	output chan any
}

type bindModelReplies struct {
	output chan any
}

func (*modelReplies) OnActivate(context.Context, *goakt.GrainProps) error   { return nil }
func (*modelReplies) OnDeactivate(context.Context, *goakt.GrainProps) error { return nil }
func (inbox *modelReplies) OnReceive(gc *goakt.GrainContext) {
	if binding, ok := gc.Message().(bindModelReplies); ok {
		inbox.output = binding.output
		gc.Response(true)
		return
	}
	inbox.output <- gc.Message()
}

func TestProviderInitializationAndRouterStream(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var requests atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
		requests.Add(1)
		if incoming.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("request did not resolve configured credential")
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer remote.Close()
	system, err := goakt.NewActorSystem("server-provider", goakt.WithPubSub(), goakt.WithLogger(log.DiscardLogger))
	if err != nil {
		t.Fatal(err)
	}
	if err := system.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer system.Stop(context.Background())
	environment := providerEnvironment{
		home: t.TempDir(),
	}
	secrets, err := initializeCredentials(ctx, system, credentialslocal.Config{
		Path: filepath.Join(environment.home, "credentials.json"),
	}, environment)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := initializeModels(ctx, system, DefaultModelConfig()); err != nil {
		t.Fatal(err)
	}
	registration, err := initializeDeepSeek(ctx, system, deepseek.Config{
		BaseURL: &remote.URL,
	}, environment, secrets)
	if err != nil || registration == "" {
		t.Fatalf("provider initialization: %q, %v", registration, err)
	}
	if requests.Load() != 0 {
		t.Fatal("initialization performed provider I/O")
	}
	// A credential added after startup is resolved by the real adapter at call time.
	if err := secrets.Set(ctx, deepseek.DefaultAPIKeyEnv, "test-key"); err != nil {
		t.Fatal(err)
	}
	registry, err := goakt.GrainOf[*llmactor.Grain](ctx, system, llmactor.Name, goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	router, err := llmactor.SpawnPool(ctx, system, "models", 2, llmactor.InitializeWorker{
		LLM: registry,
	})
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := goakt.GrainOf[*modelReplies](ctx, system, "replies", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	output := make(chan any, 8)
	if _, err := system.AskGrain(ctx, receiver, bindModelReplies{
		output: output,
	}, time.Second); err != nil {
		t.Fatal(err)
	}
	route := llmactor.Delivery{
		CallID:    "call",
		RequestID: "open",
		ReplyTo:   receiver,
	}
	exchange := func(message any) any {
		t.Helper()
		if err := goakt.Tell(ctx, router, goakt.NewBroadcast(message)); err != nil {
			t.Fatal(err)
		}
		select {
		case result := <-output:
			return result
		case <-ctx.Done():
			t.Fatal("model reply timed out")
			return nil
		}
	}
	started := exchange(llmactor.Generation{
		Delivery: route,
		Settings: llmactor.Parameters{
			Provider: deepseek.ProviderRoute,
			Model:    deepseek.DefaultModelID,
		},
		Input: llm.Input{
			SessionID: "conversation",
			Purpose:   llm.PurposeCompaction,
		},
	}).(llmactor.GenerationStarted)
	if started.Err != nil {
		t.Fatal(started.Err)
	}
	text := ""
	for position := 0; ; position++ {
		if position == 20 {
			t.Fatal("stream did not terminate")
		}
		route.RequestID = llmactor.RequestID(fmt.Sprintf("read-%d", position))
		chunk := exchange(llmactor.ReadChunk{
			Delivery: route,
		}).(llmactor.ChunkRead)
		if chunk.Err != nil {
			t.Fatal(chunk.Err)
		}
		if !chunk.Found {
			break
		}
		if delta, ok := chunk.Chunk.(llm.TextDeltaChunk); ok {
			text += delta.Text
		}
	}
	route.RequestID = "close"
	closed := exchange(llmactor.CloseCall{
		Delivery: route,
	}).(llmactor.CallClosed)
	if closed.Err != nil || text != "hello" || requests.Load() != 1 {
		t.Fatalf("stream: text=%q requests=%d close=%v", text, requests.Load(), closed.Err)
	}
}
