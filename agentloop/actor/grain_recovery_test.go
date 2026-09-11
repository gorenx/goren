package loopactor

import (
	"context"
	"testing"
	"time"

	"github.com/gorenx/goren/agent"
	middlewareactor "github.com/gorenx/goren/agent/middleware/actor"
	llmretry "github.com/gorenx/goren/llm/retry"
	retryactor "github.com/gorenx/goren/llm/retry/actor"
	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
	"github.com/tochemey/goakt/v4/reentrancy"
)

type hookCounts struct {
	recoveries int
}
type modelHooks struct{ counts hookCounts }

func (*modelHooks) OnActivate(context.Context, *goakt.GrainProps) error   { return nil }
func (*modelHooks) OnDeactivate(context.Context, *goakt.GrainProps) error { return nil }
func (owner *modelHooks) OnReceive(gc *goakt.GrainContext) {
	switch gc.Message().(type) {
	case middlewareactor.InvokeRequestError:
		owner.counts.recoveries++
		gc.Response(agent.RequestErrorResolved{
			Action: agent.RequestErrorAction{
				Retry: owner.counts.recoveries == 1,
			},
		})
	case readCounts:
		gc.Response(owner.counts)
	}
}

func TestModelRecoveryCompletesTurnThroughSelectedHandlers(t *testing.T) {
	t.Run("custom recovery", func(t *testing.T) { runModelRecovery(t, false) })
	t.Run("default retry", func(t *testing.T) { runModelRecovery(t, true) })
}

func runModelRecovery(t *testing.T, useDefault bool) {
	ctx := context.Background()
	system, err := goakt.NewActorSystem("model-request", goakt.WithLogger(log.DiscardLogger), goakt.WithPubSub())
	if err != nil {
		t.Fatal(err)
	}
	if err := system.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := system.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	subject, history := acceptedTurn(t)
	storage, err := goakt.GrainOf[*sessionGrain](ctx, system, "storage", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := system.AskGrain(ctx, storage, sessionSetup{
		history: history,
	}, time.Second); err != nil {
		t.Fatal(err)
	}
	target, err := goakt.GrainOf[*executionGrain](ctx, system, "execution", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	backend := &modelProvider{failFirst: true}
	pool := modelPool(t, system, backend)
	hooks, err := goakt.GrainOf[*modelHooks](ctx, system, "hooks", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := goakt.GrainOf[*middlewareactor.Grain](ctx, system, "middleware", goakt.WithLongLivedGrain(), goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		t.Fatal(err)
	}
	recovery := hooks
	if useDefault {
		recovery, err = goakt.GrainOf[*retryactor.Grain](ctx, system, "retry", goakt.WithLongLivedGrain(), goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
		if err != nil {
			t.Fatal(err)
		}
		initialized, err := system.AskGrain(ctx, recovery, retryactor.Initialize{
			AgentID:    "settlement",
			Session:    storage,
			CreationID: "test",
		}, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if err := initialized.(retryactor.Completion).Err; err != nil {
			t.Fatal(err)
		}
	}
	_, err = system.AskGrain(ctx, target, executionSetup{
		execute:    true,
		subject:    subject,
		target:     storage,
		middleware: coordinator,
		router:     pool,
		timeout:    time.Second,
		handlers: middlewareactor.Handlers{
			RequestError: []middlewareactor.Route{
				{
					Definition: "recover",
					Target:     recovery,
				},
			},
		},
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := system.AskGrain(ctx, target, WhenIdle{CreationID: "test"}, 5*time.Second)
	if err != nil || payload.(Completion).Err != nil {
		t.Fatalf("execution: %v, %v", payload, err)
	}
	if backend.calls.Load() != 2 {
		t.Fatalf("provider calls: %d", backend.calls.Load())
	}
	payload, err = system.AskGrain(ctx, hooks, readCounts{}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	counts := payload.(hookCounts)
	expectedRecovery := 1
	if useDefault {
		expectedRecovery = 0
	}
	if counts.recoveries != expectedRecovery {
		t.Fatalf("hook counts: %+v", counts)
	}
	payload, err = system.AskGrain(ctx, storage, sessionactor.Read{}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	view := payload.(sessionactor.View)
	if view.Err != nil {
		t.Fatal(view.Err)
	}
	chunks, messages, retries := 0, 0, 0
	for _, event := range view.Snapshot.Events {
		switch event.Type {
		case llmretry.RetryEventName, llmretry.RetryStartedEventName:
			retries++
		case session.AssistantChunkEventName:
			chunks++
		case session.AssistantMessageEventName:
			messages++
		}
	}
	if chunks != 4 || messages != 1 {
		t.Fatalf("committed output: chunks=%d messages=%d", chunks, messages)
	}
	if useDefault && retries != 2 {
		t.Fatalf("retry facts: %d", retries)
	}
}
