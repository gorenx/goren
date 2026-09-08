package questionactor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gorenx/goren/userquestions"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
)

type waitingProvider struct {
	entered   chan struct{}
	cancelled chan struct{}
	release   chan struct{}
}

func (source *waitingProvider) Ask(ctx context.Context, _ userquestions.Request) (userquestions.Answer, error) {
	close(source.entered)
	<-ctx.Done()
	close(source.cancelled)
	<-source.release
	return userquestions.Answer{}, context.Cause(ctx)
}

func questionSystem(t *testing.T) (goakt.ActorSystem, *goakt.GrainIdentity) {
	t.Helper()
	ctx := context.Background()
	system, err := goakt.NewActorSystem("question-test", goakt.WithLogger(log.DiscardLogger))
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
	target, err := goakt.GrainOf[*Grain](ctx, system, "questions", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	return system, target
}

func TestCancellationInterruptsProviderAndWaitsForCleanup(t *testing.T) {
	system, target := questionSystem(t)
	provider := &waitingProvider{
		entered:   make(chan struct{}),
		cancelled: make(chan struct{}),
		release:   make(chan struct{}),
	}
	payload, err := system.AskGrain(context.Background(), target, Initialize{
		Provider: provider,
	}, time.Second)
	if err != nil || payload.(Completion).Err != nil {
		t.Fatalf("initialize: %v %v", payload, err)
	}
	endpoint, err := NewClient(system, target)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, failure := endpoint.Ask(ctx, userquestions.Request{
			Questions: []userquestions.Question{{
				ID:       "q",
				Question: "Continue?",
			}},
		})
		done <- failure
	}()
	select {
	case <-provider.entered:
	case <-time.After(time.Second):
		t.Fatal("provider was not called")
	}
	cancel()
	select {
	case <-provider.cancelled:
	case <-time.After(time.Second):
		t.Fatal("cancellation did not reach provider")
	}
	select {
	case err := <-done:
		t.Fatalf("returned before cleanup: %v", err)
	default:
	}
	payload, err = system.AskGrain(context.Background(), target, Close{}, time.Second)
	if err != nil || payload.(Completion).Err == nil {
		t.Fatalf("close while pending: %v %v", payload, err)
	}
	close(provider.release)
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cleanup did not settle request")
	}
	payload, err = system.AskGrain(context.Background(), target, Close{}, time.Second)
	if err != nil || payload.(Completion).Err != nil {
		t.Fatalf("close: %v %v", payload, err)
	}
}
