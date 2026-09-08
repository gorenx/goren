package llmactor_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorenx/goren/llm"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
)

type providerFixture struct{ stream llm.ChunkStream }

func (backend *providerFixture) Stream(context.Context, llm.GenerateOptions) (llm.ChunkStream, error) {
	return backend.stream, nil
}

type closingStream struct {
	closes  atomic.Int32
	reading chan struct{}
	failure error
}

func (flow *closingStream) Next(ctx context.Context) (llm.StreamChunk, bool, error) {
	close(flow.reading)
	<-ctx.Done()
	return nil, false, context.Cause(ctx)
}
func (flow *closingStream) Close(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if flow.closes.Add(1) == 1 {
		return flow.failure
	}
	return nil
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

func ask[T any](t *testing.T, target *goakt.PID, message any) T {
	t.Helper()
	payload, err := goakt.Ask(context.Background(), target, message, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	result, ok := payload.(T)
	if !ok {
		t.Fatalf("unexpected response %T", payload)
	}
	return result
}
