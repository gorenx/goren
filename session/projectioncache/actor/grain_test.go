package cacheactor_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/session/projection"
	"github.com/gorenx/goren/session/projectioncache"
	cacheactor "github.com/gorenx/goren/session/projectioncache/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
	"github.com/tochemey/goakt/v4/reentrancy"
)

type operation struct {
	id      string
	target  *goakt.GrainIdentity
	message any
	timeout time.Duration
}

type outcome struct {
	id    string
	value any
	err   error
}

type requester struct {
	replies chan outcome
}

func (*requester) PreStart(*goakt.Context) error { return nil }
func (*requester) PostStop(*goakt.Context) error { return nil }
func (driver *requester) Receive(rc *goakt.ReceiveContext) {
	if input, ok := rc.Message().(operation); ok {
		replies := driver.replies
		rc.RequestGrain(input.target, input.message, goakt.WithRequestTimeout(input.timeout)).Then(func(value any, err error) {
			replies <- outcome{
				id:    input.id,
				value: value,
				err:   err,
			}
		})
	}
}

type stored struct {
	id  session.SessionID
	seq int64
}

type storage struct {
	entered chan stored
	release chan struct{}
	mu      sync.Mutex
	fail    bool
	closed  bool
}

func (*storage) LoadAll(context.Context) (map[session.SessionID]projectioncache.CheckpointRecord, error) {
	return nil, nil
}
func (backend *storage) Replace(_ context.Context, id session.SessionID, record projectioncache.CheckpointRecord) error {
	backend.entered <- stored{
		id:  id,
		seq: record.Rows["value"].Seq,
	}
	if id == "blocked" {
		<-backend.release
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.fail {
		return errors.New("injected storage failure")
	}
	return nil
}
func (backend *storage) Close(context.Context) error {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.closed = true
	return nil
}

func systemForTest(t *testing.T) (goakt.ActorSystem, *goakt.PID, <-chan outcome) {
	t.Helper()
	system, err := goakt.NewActorSystem("cache-test", goakt.WithLogger(log.DiscardLogger))
	if err != nil {
		t.Fatal(err)
	}
	if err := system.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := system.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	replies := make(chan outcome, 16)
	driver, err := system.Spawn(context.Background(), "requester", &requester{
		replies: replies,
	}, goakt.WithReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		t.Fatal(err)
	}
	return system, driver, replies
}

func openCache(t *testing.T, system goakt.ActorSystem, id string, backend projectioncache.CheckpointStore) *goakt.GrainIdentity {
	t.Helper()
	identity, err := goakt.GrainOf[*cacheactor.Grain](context.Background(), system, id, goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	result, err := system.AskGrain(context.Background(), identity, cacheactor.Initialize{
		Storage: backend,
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if failure := result.(cacheactor.Completion).Err; failure != nil {
		t.Fatal(failure)
	}
	return identity
}

func checkpoint(id session.SessionID, seq int64) cacheactor.Write {
	return cacheactor.Write{
		Header: session.Header{
			ID:        id,
			CreatedAt: 1,
		},
		Rows: projection.Checkpoint{
			"value": {
				Version: 1,
				Seq:     seq,
				Value:   json.RawMessage(`true`),
			},
		},
		Durable: session.WriteBarrier{
			SessionID: id,
			NextSeq:   seq + 1,
		},
	}
}

func send(t *testing.T, driver *goakt.PID, id string, target *goakt.GrainIdentity, message any) {
	t.Helper()
	if err := goakt.Tell(context.Background(), driver, operation{
		id:      id,
		target:  target,
		message: message,
	}); err != nil {
		t.Fatal(err)
	}
}

func receive(t *testing.T, replies <-chan outcome) outcome {
	t.Helper()
	select {
	case result := <-replies:
		if result.err != nil {
			t.Fatal(result.err)
		}
		return result
	case <-time.After(3 * time.Second):
		t.Fatal("cache request timed out")
	}
	return outcome{}
}

func TestFrameworkSerializesSameSessionWritesAndDrainsBeforeClose(t *testing.T) {
	backend := &storage{
		entered: make(chan stored, 8),
		release: make(chan struct{}),
	}
	// Always release blocked I/O before system cleanup, including failed assertions.
	system, driver, replies := systemForTest(t)
	var unblock sync.Once
	t.Cleanup(func() { unblock.Do(func() { close(backend.release) }) })
	first := openCache(t, system, "blocked", backend)
	second := openCache(t, system, "independent", backend)
	send(t, driver, "first", first, checkpoint("blocked", 0))
	select {
	case entered := <-backend.entered:
		if entered.id != "blocked" || entered.seq != 0 {
			t.Fatalf("first storage call = %#v", entered)
		}
	case <-time.After(time.Second):
		t.Fatal("first write did not enter storage")
	}
	send(t, driver, "second", first, checkpoint("blocked", 1))
	send(t, driver, "read", first, cacheactor.Read{
		Header: checkpoint("blocked", 1).Header,
	})
	send(t, driver, "close", first, cacheactor.Close{})
	send(t, driver, "other", second, checkpoint("independent", 0))
	independent := receive(t, replies)
	if independent.id != "other" || independent.value.(cacheactor.Completion).Err != nil {
		t.Fatalf("blocked Session leaked replies: %#v", independent)
	}
	entered := <-backend.entered
	if entered.id != "independent" {
		t.Fatalf("same Session ran concurrent writes: %#v", entered)
	}
	select {
	case early := <-replies:
		t.Fatalf("write/read/close replied before I/O completed: %#v", early)
	case <-time.After(20 * time.Millisecond):
	}
	unblock.Do(func() { close(backend.release) })
	for _, expected := range []string{"first", "second", "read", "close"} {
		result := receive(t, replies)
		if result.id != expected {
			t.Fatalf("reply order = %s, want %s", result.id, expected)
		}
		if expected == "read" {
			record := result.value.(cacheactor.Record)
			if record.Err != nil || record.Value == nil || record.Value.Rows["value"].Seq != 1 {
				t.Fatalf("record = %#v", record)
			}
		} else if failure := result.value.(cacheactor.Completion).Err; failure != nil {
			t.Fatal(failure)
		}
	}
	backend.mu.Lock()
	sharedClosed := backend.closed
	backend.mu.Unlock()
	if sharedClosed {
		t.Fatal("Session cache closed the shared storage adapter")
	}
}

func TestFailedWriteDoesNotPublishRecordAndCanBeRetried(t *testing.T) {
	backend := &storage{
		entered: make(chan stored, 8),
		fail:    true,
	}
	system, driver, replies := systemForTest(t)
	identity := openCache(t, system, "retry", backend)
	send(t, driver, "failed", identity, checkpoint("retry", 0))
	if result := receive(t, replies); result.value.(cacheactor.Completion).Err == nil {
		t.Fatal("storage failure was hidden")
	}
	send(t, driver, "read", identity, cacheactor.Read{
		Header: checkpoint("retry", 0).Header,
	})
	record := receive(t, replies).value.(cacheactor.Record)
	if record.Err != nil || record.Value != nil {
		t.Fatalf("failed write published record: %#v", record)
	}
	backend.mu.Lock()
	backend.fail = false
	backend.mu.Unlock()
	send(t, driver, "retry", identity, checkpoint("retry", 0))
	if result := receive(t, replies); result.value.(cacheactor.Completion).Err != nil {
		t.Fatal(result.value)
	}
	send(t, driver, "confirmed", identity, cacheactor.Read{
		Header: checkpoint("retry", 0).Header,
	})
	record = receive(t, replies).value.(cacheactor.Record)
	if record.Err != nil || record.Value == nil || record.Value.Rows["value"].Seq != 0 {
		t.Fatalf("retry = %#v", record)
	}
}

func TestCallerTimeoutDoesNotReleaseUnderlyingWriteBarrier(t *testing.T) {
	backend := &storage{
		entered: make(chan stored, 8),
		release: make(chan struct{}),
	}
	system, driver, replies := systemForTest(t)
	var unblock sync.Once
	t.Cleanup(func() { unblock.Do(func() { close(backend.release) }) })
	identity := openCache(t, system, "blocked", backend)
	if err := goakt.Tell(context.Background(), driver, operation{
		id:      "timeout",
		target:  identity,
		message: checkpoint("blocked", 0),
		timeout: 50 * time.Millisecond,
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-backend.entered:
	case <-time.After(time.Second):
		t.Fatal("storage did not start")
	}
	select {
	case result := <-replies:
		if result.id != "timeout" || result.err == nil {
			t.Fatalf("outer timeout = %#v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("outer request did not time out")
	}
	send(t, driver, "close", identity, cacheactor.Close{})
	select {
	case result := <-replies:
		t.Fatalf("close ignored underlying write: %#v", result)
	case <-time.After(20 * time.Millisecond):
	}
	unblock.Do(func() { close(backend.release) })
	closed := receive(t, replies)
	if closed.id != "close" || closed.value.(cacheactor.Completion).Err != nil {
		t.Fatalf("close = %#v", closed)
	}
}
