package llmactor_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gorenx/goren/llm"
	llmactor "github.com/gorenx/goren/llm/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
)

type resultInbox struct{ results chan any }
type bindInbox struct{ results chan any }

func (*resultInbox) OnActivate(context.Context, *goakt.GrainProps) error   { return nil }
func (*resultInbox) OnDeactivate(context.Context, *goakt.GrainProps) error { return nil }
func (sink *resultInbox) OnReceive(gc *goakt.GrainContext) {
	if input, ok := gc.Message().(bindInbox); ok {
		sink.results = input.results
		gc.Response(true)
		return
	}
	sink.results <- gc.Message()
	gc.NoErr()
}

func receiveResult[T any](t *testing.T, results <-chan any) T {
	t.Helper()
	select {
	case payload := <-results:
		result, ok := payload.(T)
		if !ok {
			t.Fatalf("unexpected result %T", payload)
		}
		return result
	case <-time.After(3 * time.Second):
		t.Fatal("result did not arrive at receiving Grain")
	}
	var zero T
	return zero
}

func TestRouterWorkerCancelsBlockedReadAndReusesPool(t *testing.T) {
	system := startSystem(t, goakt.WithPubSub())
	ctx := context.Background()
	registry, err := goakt.GrainOf[*llmactor.Grain](ctx, system, "registry", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	settings := registry
	receiver, err := goakt.GrainOf[*resultInbox](ctx, system, "results", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan any, 8)
	if _, err := system.AskGrain(ctx, receiver, bindInbox{
		results: results,
	}, time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := system.AskGrain(ctx, settings, llmactor.SetConfiguration{
		AgentID: "a",
		Config: llm.CallConfig{
			Provider: "p",
			Model:    "m",
		},
	}, time.Second); err != nil {
		t.Fatal(err)
	}
	pool, err := llmactor.SpawnPool(ctx, system, "pool", 1, llmactor.InitializeWorker{
		LLM: registry,
	})
	if err != nil {
		t.Fatal(err)
	}
	before := ask[*goakt.Routees](t, pool, &goakt.GetRoutees{}).Names()
	for _, id := range []llmactor.CallID{"first", "second"} {
		flow := &closingStream{
			reading: make(chan struct{}),
			failure: errors.New("close temporarily failed"),
		}
		payload, err := system.AskGrain(ctx, registry, llmactor.RegisterAdapter{
			Providers: []string{"p"},
			Adapter: &providerFixture{
				stream: flow,
			},
		}, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		registered := payload.(llmactor.Registered)
		if registered.Err != nil {
			t.Fatal(registered.Err)
		}
		route := llmactor.Delivery{
			CallID:    id,
			RequestID: "generate",
			ReplyTo:   receiver,
		}
		send := func(message any) {
			t.Helper()
			if err := goakt.Tell(ctx, pool, goakt.NewBroadcast(message)); err != nil {
				t.Fatal(err)
			}
		}
		send(llmactor.Generation{
			Delivery: route,
			Settings: llmactor.AgentConfig{
				AgentID: "a",
			},
		})
		started := receiveResult[llmactor.GenerationStarted](t, results)
		if started.Err != nil || started.Config.Model != "m" {
			t.Fatalf("start: %+v", started)
		}
		route.RequestID = "read"
		send(llmactor.ReadChunk{
			Delivery: route,
		})
		select {
		case <-flow.reading:
		case <-time.After(time.Second):
			select {
			case result := <-results:
				t.Fatalf("provider read did not start; result: %#v", result)
			default:
				t.Fatal("provider read did not start; no result")
			}
		}
		route.RequestID = "cancel"
		cause := errors.New("Agent canceled")
		send(llmactor.CancelCall{
			Delivery: route,
			Cause:    cause,
		})
		accepted, read := false, false
		for range 2 {
			select {
			case payload := <-results:
				switch result := payload.(type) {
				case llmactor.CancellationAccepted:
					if result.Err != nil || accepted {
						t.Fatalf("cancel: %+v", result)
					}
					accepted = true
				case llmactor.ChunkRead:
					if !errors.Is(result.Err, cause) || read {
						t.Fatalf("read: %+v", result)
					}
					read = true
				default:
					t.Fatalf("unexpected result %T", payload)
				}
			case <-time.After(time.Second):
				t.Fatal("cancel or read result missing")
			}
		}
		if !accepted || !read {
			t.Fatal("cancel and read must both complete")
		}
		route.RequestID = "close"
		send(llmactor.CloseCall{
			Delivery: route,
		})
		if closed := receiveResult[llmactor.CallClosed](t, results); !errors.Is(closed.Err, flow.failure) {
			t.Fatalf("first close: %v", closed.Err)
		}
		route.RequestID = "retry-close"
		send(llmactor.CloseCall{
			Delivery: route,
		})
		if closed := receiveResult[llmactor.CallClosed](t, results); closed.Err != nil {
			t.Fatal(closed.Err)
		}
		if _, err := system.AskGrain(ctx, registry, llmactor.RemoveAdapter{
			ID: registered.ID,
		}, time.Second); err != nil {
			t.Fatal(err)
		}
	}
	after := ask[*goakt.Routees](t, pool, &goakt.GetRoutees{}).Names()
	if len(before) != 1 || len(after) != 1 || before[0] != after[0] {
		t.Fatalf("pool was recreated: %v -> %v", before, after)
	}
}
