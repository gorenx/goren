package persistence

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/gorenx/goren/session"
)

type exactStateWriteTarget struct {
	operations *sessionGates
	durable    *durableState
	identifier session.SessionID
	started    chan struct{}
	release    chan struct{}
	once       sync.Once
}

func (target *exactStateWriteTarget) WriteBatch(
	requestContext context.Context,
	events []session.Event,
) error {
	releaseGate, err := target.operations.Acquire(requestContext, target.identifier)
	if err != nil {
		return err
	}
	defer releaseGate()
	target.once.Do(func() {
		close(target.started)
	})
	<-target.release
	target.durable.cursor += int64(len(events))
	return nil
}

func (*exactStateWriteTarget) ReportBackgroundFailure(error) {}

func TestFlushKeepsExactDurableState(t *testing.T) {
	conversation, err := session.New("exact-flush", session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	tracked := &durableState{
		metadata: conversation.Header(),
		owner:    conversation,
	}
	operations := newSessionGates()
	target := &exactStateWriteTarget{
		operations: operations,
		durable:    tracked,
		identifier: conversation.ID(),
		started:    make(chan struct{}),
		release:    make(chan struct{}),
	}
	live := &liveSessionState{
		conversation: conversation,
		durable:      tracked,
	}
	live.writes = newLiveWriter(time.Hour, target)
	live.writes.enqueue(session.Event{
		Type: "test/exact-state",
		Seq:  0,
		Time: 1,
		Data: []byte(`{}`),
	})
	owner := &SessionLogStore{
		durable:    newDurableSessions(),
		writes:     newLiveWrites(),
		operations: operations,
	}
	owner.durable.Put(conversation.ID(), tracked)
	owner.writes.Put(conversation, live)
	flushDone := make(chan error, 1)
	go func() {
		flushDone <- owner.onFlush(
			context.Background(),
			conversation,
			session.WriteBarrier{
				SessionID: conversation.ID(),
				NextSeq:   1,
			},
		)
	}()
	<-target.started
	owner.durable.mutex.Lock()
	delete(owner.durable.entries, conversation.ID())
	owner.durable.mutex.Unlock()
	close(target.release)
	if err := <-flushDone; err != nil {
		t.Fatalf("Flush re-resolved durable state by ID: %v", err)
	}
	if tracked.cursor != 1 {
		t.Fatalf("exact durable cursor = %d", tracked.cursor)
	}
}

func TestDisposedReleasesOnlyExactLiveOwner(t *testing.T) {
	conversation, err := session.New("exact-disposed", session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	tracked := &durableState{
		metadata: conversation.Header(),
		cursor:   3,
		owner:    conversation,
	}
	live := &liveSessionState{
		conversation: conversation,
		durable:      tracked,
		writes: newLiveWriter(
			time.Hour,
			&exactStateWriteTarget{
				operations: newSessionGates(),
				durable:    tracked,
				identifier: conversation.ID(),
				started:    make(chan struct{}),
				release:    make(chan struct{}),
			},
		),
	}
	owner := &SessionLogStore{
		durable:    newDurableSessions(),
		writes:     newLiveWrites(),
		operations: newSessionGates(),
	}
	owner.durable.Put(conversation.ID(), tracked)
	owner.writes.Put(conversation, live)
	if err := owner.onDisposed(context.Background(), conversation); err != nil {
		t.Fatal(err)
	}
	retained, found := owner.durable.Get(conversation.ID())
	if !found || retained != tracked || retained.cursor != 3 || retained.owner != nil {
		t.Fatalf("retained durable state = %#v, found %v", retained, found)
	}
	if _, found := owner.writes.Get(conversation); found {
		t.Fatal("Disposed retained exact live writer")
	}
}
