package persistence

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorenx/goren/session"
)

func TestLiveWriterRetainsFailedBackgroundBatchForExplicitFlush(t *testing.T) {
	t.Parallel()
	backgroundFailure := errors.New("background failure")
	reported := make(chan error, 1)
	var attempts atomic.Int32
	var persisted []session.Event
	writes := newLiveWriter(time.Millisecond, func(_ context.Context, entries []session.Event) error {
		if attempts.Add(1) == 1 {
			return backgroundFailure
		}
		persisted = snapshotEvents(entries)
		return nil
	}, func(problem error) {
		reported <- problem
	})
	writes.enqueue(session.Event{Type: "probe/event", Seq: 0, Time: 1, Data: []byte(`{}`), Ignorable: true})
	select {
	case problem := <-reported:
		if !errors.Is(problem, backgroundFailure) {
			t.Fatalf("reported error = %v", problem)
		}
	case <-time.After(time.Second):
		t.Fatal("background failure was not reported")
	}
	if err := writes.flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if attempts.Load() != 2 || len(persisted) != 1 || persisted[0].Seq != 0 || writes.hasWork() {
		t.Fatalf("retry state = attempts %d, persisted %#v, hasWork %t", attempts.Load(), persisted, writes.hasWork())
	}
}

func TestLiveWriterFlushCanCancelWhileBackgroundWriteRemainsOwned(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	allowCompletion := make(chan struct{})
	writes := newLiveWriter(time.Millisecond, func(_ context.Context, _ []session.Event) error {
		close(started)
		<-allowCompletion
		return nil
	}, func(error) {})
	writes.enqueue(session.Event{Type: "probe/event", Seq: 0, Time: 1, Data: []byte(`{}`), Ignorable: true})
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("background write did not start")
	}
	flushContext, cancel := context.WithCancel(context.Background())
	cancel()
	if err := writes.flush(flushContext); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled flush error = %v", err)
	}
	close(allowCompletion)
	if err := writes.flush(context.Background()); err != nil {
		t.Fatal(err)
	}
}
