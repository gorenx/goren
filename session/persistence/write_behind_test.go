package persistence

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorenx/goren/session"
)

type liveWriterTargetFixture struct {
	attempts      atomic.Int32
	backgroundErr error
	reported      chan error
	started       chan struct{}
	allowWrite    chan struct{}
	startOnce     sync.Once
	persisted     []session.Event
}

func (fixture *liveWriterTargetFixture) WriteBatch(
	_ context.Context,
	entries []session.Event,
) error {
	fixture.startOnce.Do(func() {
		if fixture.started != nil {
			close(fixture.started)
		}
	})
	if fixture.allowWrite != nil {
		<-fixture.allowWrite
	}
	if fixture.attempts.Add(1) == 1 && fixture.backgroundErr != nil {
		return fixture.backgroundErr
	}
	fixture.persisted = snapshotEvents(entries)
	return nil
}

func (fixture *liveWriterTargetFixture) ReportBackgroundFailure(problem error) {
	if fixture.reported != nil {
		fixture.reported <- problem
	}
}

func TestLiveWriterRetainsFailedBackgroundBatchForExplicitFlush(t *testing.T) {
	t.Parallel()
	backgroundFailure := errors.New("background failure")
	target := &liveWriterTargetFixture{
		backgroundErr: backgroundFailure,
		reported:      make(chan error, 1),
	}
	writes := newLiveWriter(time.Millisecond, target)
	writes.enqueue(
		session.Event{
			Type:      "probe/event",
			Seq:       0,
			Time:      1,
			Data:      []byte(`{}`),
			Ignorable: true,
		},
	)
	select {
	case problem := <-target.reported:
		if !errors.Is(problem, backgroundFailure) {
			t.Fatalf("reported error = %v", problem)
		}
	case <-time.After(time.Second):
		t.Fatal("background failure was not reported")
	}
	if err := writes.flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if target.attempts.Load() != 2 || len(target.persisted) != 1 ||
		target.persisted[0].Seq != 0 || writes.hasWork() {
		t.Fatalf(
			"retry state = attempts %d, persisted %#v, hasWork %t",
			target.attempts.Load(),
			target.persisted,
			writes.hasWork(),
		)
	}
}

func TestLiveWriterFlushCanCancelWhileBackgroundWriteRemainsOwned(t *testing.T) {
	t.Parallel()
	target := &liveWriterTargetFixture{
		started:    make(chan struct{}),
		allowWrite: make(chan struct{}),
	}
	writes := newLiveWriter(time.Millisecond, target)
	writes.enqueue(
		session.Event{
			Type:      "probe/event",
			Seq:       0,
			Time:      1,
			Data:      []byte(`{}`),
			Ignorable: true,
		},
	)
	select {
	case <-target.started:
	case <-time.After(time.Second):
		t.Fatal("background write did not start")
	}
	flushContext, cancel := context.WithCancel(context.Background())
	cancel()
	if err := writes.flush(flushContext); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled flush error = %v", err)
	}
	close(target.allowWrite)
	if err := writes.flush(context.Background()); err != nil {
		t.Fatal(err)
	}
}
