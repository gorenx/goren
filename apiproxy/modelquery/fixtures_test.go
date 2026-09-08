package modelquery_test

import (
	"context"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
	"testing"
	"time"
)

type response struct {
	payload any
	err     error
}
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

func startSystem(t *testing.T, options ...goakt.Option) goakt.ActorSystem {
	t.Helper()
	system, err := goakt.NewActorSystem("llm-test", append([]goakt.Option{goakt.WithLogger(log.DiscardLogger)}, options...)...)
	if err != nil {
		t.Fatal(err)
	}
	if err := system.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := system.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return system
}
