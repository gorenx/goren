package spike

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

type storeInit struct{ Path string }
type storeWrite struct {
	Text string
	Gate <-chan struct{}
}
type storeFlush struct {
	Barrier    int
	Gate       <-chan struct{}
	ClosedFile bool
}
type storeClose struct{}
type storeSnapshot struct {
	Committed int
	Queued    int
	Flushed   int
	Closed    bool
	Busy      bool
}
type storeOperation struct {
	Text       string
	Flush      bool
	Barrier    int
	Gate       <-chan struct{}
	ClosedFile bool
	Reply      *actor.GrainReply
}
type storeCompleted struct{ Failure string }

// JournalGrain serializes real append and fsync operations. Barrier semantics
// belong here; the goroutine only performs the requested file operation.
type JournalGrain struct {
	path   string
	queue  []storeOperation
	active *storeOperation
	state  storeSnapshot
}

func (*JournalGrain) OnActivate(context.Context, *actor.GrainProps) error   { return nil }
func (*JournalGrain) OnDeactivate(context.Context, *actor.GrainProps) error { return nil }
func (owner *JournalGrain) OnReceive(gc *actor.GrainContext) {
	switch incoming := gc.Message().(type) {
	case storeInit:
		owner.path = incoming.Path
		gc.Response(true)
	case storeWrite:
		if owner.state.Closed || owner.path == "" {
			gc.Response("rejected")
			return
		}
		owner.queue = append(owner.queue, storeOperation{
			Text:  incoming.Text,
			Gate:  incoming.Gate,
			Reply: gc.DeferResponse(),
		})
		owner.advance(gc)
	case storeFlush:
		if owner.state.Closed || owner.path == "" {
			gc.Response("rejected")
			return
		}
		owner.queue = append(owner.queue, storeOperation{
			Flush:      true,
			Barrier:    incoming.Barrier,
			Gate:       incoming.Gate,
			ClosedFile: incoming.ClosedFile,
			Reply:      gc.DeferResponse(),
		})
		owner.advance(gc)
	case storeCompleted:
		active := owner.active
		if active == nil {
			return
		}
		owner.active = nil
		owner.state.Busy = false
		if incoming.Failure != "" {
			active.Reply.Response(incoming)
		} else {
			if active.Flush {
				owner.state.Flushed = owner.state.Committed
			} else {
				owner.state.Committed++
			}
			active.Reply.Response(owner.state)
		}
		owner.advance(gc)
	case storeClose:
		if owner.state.Busy || len(owner.queue) > 0 || owner.state.Flushed < owner.state.Committed {
			gc.Response("not-quiesced")
			return
		}
		owner.state.Closed = true
		gc.Response("session-closed")
	case requestProbe:
		snapshot := owner.state
		snapshot.Queued = len(owner.queue)
		gc.Response(snapshot)
	}
	gc.NoErr()
}
func (owner *JournalGrain) advance(gc *actor.GrainContext) {
	if owner.active != nil || len(owner.queue) == 0 {
		return
	}
	current := owner.queue[0]
	owner.queue = owner.queue[1:]
	if current.Flush && current.Barrier > owner.state.Committed {
		current.Reply.Response(storeCompleted{
			Failure: "unknown barrier",
		})
		owner.advance(gc)
		return
	}
	owner.active = &current
	owner.state.Busy = true
	path := owner.path
	if failure := gc.PipeToSelf(func() (any, error) {
		if current.Gate != nil {
			<-current.Gate
		}
		flags := os.O_WRONLY | os.O_APPEND
		if current.Flush {
			flags = os.O_RDWR
		}
		handle, failure := os.OpenFile(path, flags, 0600)
		if failure == nil {
			if current.Flush {
				if current.ClosedFile {
					_ = handle.Close()
				}
				failure = handle.Sync()
			} else {
				_, failure = handle.WriteString(current.Text + "\n")
			}
			closeFailure := handle.Close()
			if failure == nil {
				failure = closeFailure
			}
		}
		completed := storeCompleted{}
		if failure != nil {
			completed.Failure = failure.Error()
		}
		return completed, nil
	}); failure != nil {
		owner.active = nil
		owner.state.Busy = false
		current.Reply.Response(storeCompleted{
			Failure: failure.Error(),
		})
		owner.advance(gc)
	}
}

type lifecycleInit struct{ Journal *actor.GrainIdentity }
type lifecycleWork struct {
	Label  string
	Update bool
	Gate   <-chan struct{}
}
type lifecycleEntry struct {
	Work  lifecycleWork
	Reply *actor.GrainReply
}
type lifecycleReady struct{}
type lifecycleCommitted struct {
	Snapshot storeSnapshot
	Failure  string
}
type lifecycleFlushed struct {
	Snapshot storeSnapshot
	Failure  string
}
type lifecycleClose struct {
	Gate       <-chan struct{}
	ClosedFile bool
}
type lifecycleSnapshot struct {
	Phase    string
	Revision int
	Queued   int
	Closing  bool
	History  []string
}

// LifecycleGrain models a single Agent business FIFO. An update gate represents
// preparation/retirement completion; it is not a Tools Registry transaction.
type LifecycleGrain struct {
	journal     *actor.GrainIdentity
	entries     []lifecycleEntry
	active      *lifecycleEntry
	closer      *actor.GrainReply
	flushConfig lifecycleClose
	barrier     int
	state       lifecycleSnapshot
}

func (owner *LifecycleGrain) OnActivate(context.Context, *actor.GrainProps) error {
	owner.state.Phase = "idle"
	owner.state.Revision = 1
	return nil
}
func (*LifecycleGrain) OnDeactivate(context.Context, *actor.GrainProps) error { return nil }
func (owner *LifecycleGrain) OnReceive(gc *actor.GrainContext) {
	switch incoming := gc.Message().(type) {
	case lifecycleInit:
		owner.journal = incoming.Journal
		gc.Response(true)
	case lifecycleWork:
		if owner.state.Closing || owner.journal == nil {
			gc.Response("rejected")
			return
		}
		owner.entries = append(owner.entries, lifecycleEntry{
			Work:  incoming,
			Reply: gc.DeferResponse(),
		})
		owner.advance(gc)
	case lifecycleReady:
		if owner.active == nil {
			return
		}
		if owner.active.Work.Update {
			owner.state.Revision++
			owner.state.History = append(owner.state.History, "updated")
			owner.active.Reply.Response("updated")
			owner.active = nil
			owner.state.Phase = "idle"
			owner.advance(gc)
			return
		}
		text := fmt.Sprintf("%s:v%d", owner.active.Work.Label, owner.state.Revision)
		owner.state.Phase = "committing"
		completionFeed := make(chan lifecycleCommitted, 1)
		if failure := gc.PipeToSelf(func() (any, error) { return <-completionFeed, nil }); failure != nil {
			owner.active.Reply.Err(failure)
			return
		}
		gc.RequestGrain(owner.journal, storeWrite{
			Text: text,
		}, actor.WithRequestTimeout(2*time.Second)).Then(func(payload any, failure error) {
			completion := lifecycleCommitted{}
			if failure != nil {
				completion.Failure = failure.Error()
			} else {
				switch result := payload.(type) {
				case storeSnapshot:
					completion.Snapshot = result
				case storeCompleted:
					completion.Failure = result.Failure
				default:
					completion.Failure = "unexpected commit reply"
				}
			}
			completionFeed <- completion
		})
	case lifecycleCommitted:
		if owner.active == nil {
			return
		}
		if incoming.Failure != "" {
			owner.active.Reply.Response(incoming)
			owner.state.Phase = "failed"
			return
		}
		owner.barrier = incoming.Snapshot.Committed
		owner.state.History = append(owner.state.History, fmt.Sprintf("%s:v%d", owner.active.Work.Label, owner.state.Revision))
		owner.active.Reply.Response("committed")
		owner.active = nil
		owner.state.Phase = "idle"
		owner.advance(gc)
	case lifecycleClose:
		if owner.state.Phase == "quiesced" {
			gc.Response("quiesced")
			return
		}
		if owner.closer != nil {
			gc.Response("already-closing")
			return
		}
		owner.state.Closing = true
		owner.closer = gc.DeferResponse()
		owner.flushConfig = incoming
		owner.advance(gc)
	case lifecycleFlushed:
		if owner.closer == nil {
			return
		}
		if incoming.Failure != "" {
			owner.state.Phase = "flush-failed"
			owner.closer.Response(incoming)
		} else {
			owner.state.Phase = "quiesced"
			owner.closer.Response("quiesced")
		}
		owner.closer = nil
	case requestProbe:
		snapshot := owner.state
		snapshot.Queued = len(owner.entries)
		snapshot.History = append([]string(nil), owner.state.History...)
		gc.Response(snapshot)
	}
	gc.NoErr()
}
func (owner *LifecycleGrain) advance(gc *actor.GrainContext) {
	if owner.active != nil || owner.state.Phase == "flushing" {
		return
	}
	if len(owner.entries) > 0 {
		next := owner.entries[0]
		owner.entries = owner.entries[1:]
		owner.active = &next
		owner.state.Phase = "working"
		if next.Work.Update {
			owner.state.Phase = "updating"
		}
		gate := next.Work.Gate
		if failure := gc.PipeToSelf(func() (any, error) {
			if gate != nil {
				<-gate
			}
			return lifecycleReady{}, nil
		}); failure != nil {
			owner.state.Phase = "failed"
			next.Reply.Err(failure)
		}
		return
	}
	if owner.closer == nil {
		return
	}
	owner.state.Phase = "flushing"
	completionFeed := make(chan lifecycleFlushed, 1)
	if failure := gc.PipeToSelf(func() (any, error) { return <-completionFeed, nil }); failure != nil {
		owner.closer.Err(failure)
		return
	}
	gc.RequestGrain(owner.journal, storeFlush{
		Barrier:    owner.barrier,
		Gate:       owner.flushConfig.Gate,
		ClosedFile: owner.flushConfig.ClosedFile,
	}, actor.WithRequestTimeout(2*time.Second)).Then(func(payload any, failure error) {
		completion := lifecycleFlushed{}
		if failure != nil {
			completion.Failure = failure.Error()
		} else {
			switch result := payload.(type) {
			case storeSnapshot:
				completion.Snapshot = result
			case storeCompleted:
				completion.Failure = result.Failure
			default:
				completion.Failure = "unexpected flush reply"
			}
		}
		completionFeed <- completion
	})
}

type lifecycleFixture struct {
	Harness requestHarness
	Agent   *actor.GrainIdentity
	Journal *actor.GrainIdentity
	Path    string
}

func newLifecycleFixture(t *testing.T) lifecycleFixture {
	t.Helper()
	fixture := newRequestHarness(t)
	path := t.TempDir() + "/session.log"
	if failure := os.WriteFile(path, nil, 0600); failure != nil {
		t.Fatal(failure)
	}
	options := []actor.GrainOption{actor.WithLongLivedGrain(), actor.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll)))}
	journalID, failure := actor.GrainOf[*JournalGrain](context.Background(), fixture.system, "session-42", options...)
	if failure != nil {
		t.Fatal(failure)
	}
	agentID, failure := actor.GrainOf[*LifecycleGrain](context.Background(), fixture.system, "session-42", options...)
	if failure != nil {
		t.Fatal(failure)
	}
	if journalID.String() == agentID.String() {
		t.Fatal("different owner types collided on same ID")
	}
	inspectGrain(t, fixture.system, journalID, storeInit{
		Path: path,
	})
	inspectGrain(t, fixture.system, agentID, lifecycleInit{
		Journal: journalID,
	})
	return lifecycleFixture{
		Harness: fixture,
		Agent:   agentID,
		Journal: journalID,
		Path:    path,
	}
}
func asyncGrainCall(t *testing.T, running actor.ActorSystem, name string, target *actor.GrainIdentity, payload any) <-chan requestOutcome {
	t.Helper()
	feed := make(chan requestOutcome, 2)
	clientPID := spawnRequestActor(t, running, name, &requestClient{
		outcomes: feed,
	})
	sendRequestMessage(t, clientPID, grainRequestStart{
		Target:  target,
		Payload: payload,
		Timeout: 5 * time.Second,
	})
	return feed
}
func awaitLifecycle(t *testing.T, fixture lifecycleFixture, phase string, queued int) lifecycleSnapshot {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		observed := inspectGrain(t, fixture.Harness.system, fixture.Agent, requestProbe{}).(lifecycleSnapshot)
		if observed.Phase == phase && (queued < 0 || observed.Queued == queued) {
			return observed
		}
		if time.Now().After(deadline) {
			t.Fatalf("wanted %s queue %d; got %+v", phase, queued, observed)
		}
		time.Sleep(time.Millisecond)
	}
}
func assertPending(t *testing.T, feed <-chan requestOutcome) {
	t.Helper()
	select {
	case outcome := <-feed:
		t.Fatalf("premature completion: %+v", outcome)
	default:
	}
}
func assertPayload(t *testing.T, feed <-chan requestOutcome, want string) {
	t.Helper()
	outcome := readRequestOutcome(t, feed)
	if outcome.Err != nil || outcome.Payload != want {
		t.Fatalf("wanted %s: %+v", want, outcome)
	}
}
func TestBusinessUpdateBarrierAndCloseAfterSessionFlush(t *testing.T) {
	fixture := newLifecycleFixture(t)
	running := fixture.Harness.system
	firstGate, updateGate, flushGate := make(chan struct{}), make(chan struct{}), make(chan struct{})
	// Cleanup must release test tasks even after an assertion fails.
	releaseFirst := onceGate(firstGate)
	releaseUpdate := onceGate(updateGate)
	releaseFlush := onceGate(flushGate)
	t.Cleanup(releaseFirst)
	t.Cleanup(releaseUpdate)
	t.Cleanup(releaseFlush)
	first := asyncGrainCall(t, running, "first", fixture.Agent, lifecycleWork{
		Label: "A",
		Gate:  firstGate,
	})
	awaitLifecycle(t, fixture, "working", 0)
	update := asyncGrainCall(t, running, "update", fixture.Agent, lifecycleWork{
		Update: true,
		Gate:   updateGate,
	})
	awaitLifecycle(t, fixture, "working", 1)
	second := asyncGrainCall(t, running, "second", fixture.Agent, lifecycleWork{
		Label: "B",
	})
	awaitLifecycle(t, fixture, "working", 2)
	closed := asyncGrainCall(t, running, "close", fixture.Agent, lifecycleClose{
		Gate: flushGate,
	})
	deadline := time.Now().Add(time.Second)
	for !inspectGrain(t, running, fixture.Agent, requestProbe{}).(lifecycleSnapshot).Closing {
		if time.Now().After(deadline) {
			t.Fatal("close not admitted")
		}
		time.Sleep(time.Millisecond)
	}
	if reply := inspectGrain(t, running, fixture.Agent, lifecycleWork{
		Label: "C",
	}); reply != "rejected" {
		t.Fatal("close admitted new work")
	}
	assertPending(t, first)
	assertPending(t, update)
	assertPending(t, second)
	assertPending(t, closed)
	releaseFirst()
	assertPayload(t, first, "committed")
	observed := awaitLifecycle(t, fixture, "updating", 1)
	if observed.Revision != 1 || strings.Join(observed.History, ",") != "A:v1" {
		t.Fatalf("update published early: %+v", observed)
	}
	assertPending(t, update)
	assertPending(t, second)
	assertPending(t, closed)
	releaseUpdate()
	assertPayload(t, update, "updated")
	assertPayload(t, second, "committed")
	observed = awaitLifecycle(t, fixture, "flushing", 0)
	if strings.Join(observed.History, ",") != "A:v1,updated,B:v2" {
		t.Fatalf("business order: %+v", observed)
	}
	journal := inspectGrain(t, running, fixture.Journal, requestProbe{}).(storeSnapshot)
	if journal.Committed != 2 || journal.Flushed != 0 || journal.Closed {
		t.Fatalf("commit/flush conflated: %+v", journal)
	}
	if reply := inspectGrain(t, running, fixture.Journal, storeClose{}); reply != "not-quiesced" {
		t.Fatal("session closed before flush")
	}
	assertPending(t, closed)
	releaseFlush()
	assertPayload(t, closed, "quiesced")
	awaitLifecycle(t, fixture, "quiesced", 0)
	journal = inspectGrain(t, running, fixture.Journal, requestProbe{}).(storeSnapshot)
	if journal.Flushed != 2 {
		t.Fatalf("close without barrier: %+v", journal)
	}
	if reply := inspectGrain(t, running, fixture.Journal, storeClose{}); reply != "session-closed" {
		t.Fatal("session close failed")
	}
	content, failure := os.ReadFile(fixture.Path)
	if failure != nil || string(content) != "A:v1\nB:v2\n" {
		t.Fatalf("file contents %q: %v", content, failure)
	}
}
func onceGate(gate chan struct{}) func() {
	var once sync.Once
	return func() { once.Do(func() { close(gate) }) }
}
func TestSessionFlushFailureRetainsCloseAndCanRetry(t *testing.T) {
	fixture := newLifecycleFixture(t)
	running := fixture.Harness.system
	written := asyncGrainCall(t, running, "write", fixture.Agent, lifecycleWork{
		Label: "A",
	})
	assertPayload(t, written, "committed")
	closed := asyncGrainCall(t, running, "bad-close", fixture.Agent, lifecycleClose{
		ClosedFile: true,
	})
	outcome := readRequestOutcome(t, closed)
	if outcome.Err != nil {
		t.Fatal(outcome.Err)
	}
	failed, ok := outcome.Payload.(lifecycleFlushed)
	if !ok || failed.Failure == "" {
		t.Fatalf("Sync failure hidden: %+v", outcome)
	}
	observed := awaitLifecycle(t, fixture, "flush-failed", 0)
	if !observed.Closing {
		t.Fatal("close failure reopened admission")
	}
	journal := inspectGrain(t, running, fixture.Journal, requestProbe{}).(storeSnapshot)
	if journal.Committed != 1 || journal.Flushed != 0 || journal.Closed {
		t.Fatalf("failed flush changed committed facts: %+v", journal)
	}
	retry := asyncGrainCall(t, running, "retry-close", fixture.Agent, lifecycleClose{})
	assertPayload(t, retry, "quiesced")
	journal = inspectGrain(t, running, fixture.Journal, requestProbe{}).(storeSnapshot)
	if journal.Committed != 1 || journal.Flushed != 1 {
		t.Fatalf("retry duplicated commit: %+v", journal)
	}
}

func TestSessionFIFOFlushBarrier(t *testing.T) {
	fixture := newLifecycleFixture(t)
	running := fixture.Harness.system
	gate := make(chan struct{})
	release := onceGate(gate)
	t.Cleanup(release)
	first := asyncGrainCall(t, running, "fifo-first", fixture.Journal, storeWrite{
		Text: "first",
		Gate: gate,
	})
	waitQueue := func(want int) {
		t.Helper()
		deadline := time.Now().Add(time.Second)
		for {
			observed := inspectGrain(t, running, fixture.Journal, requestProbe{}).(storeSnapshot)
			if observed.Busy && observed.Queued == want {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("queue: %+v", observed)
			}
			time.Sleep(time.Millisecond)
		}
	}
	waitQueue(0)
	second := asyncGrainCall(t, running, "fifo-second", fixture.Journal, storeWrite{
		Text: "second",
	})
	waitQueue(1)
	flushed := asyncGrainCall(t, running, "fifo-flush", fixture.Journal, storeFlush{
		Barrier: 2,
	})
	waitQueue(2)
	assertPending(t, second)
	assertPending(t, flushed)
	release()
	for index, feed := range []<-chan requestOutcome{first, second, flushed} {
		outcome := readRequestOutcome(t, feed)
		if outcome.Err != nil {
			t.Fatal(outcome.Err)
		}
		observed := outcome.Payload.(storeSnapshot)
		if index < 2 && observed.Committed != index+1 {
			t.Fatalf("commit order: %+v", observed)
		}
		if index == 2 && observed.Flushed != 2 {
			t.Fatalf("flush barrier: %+v", observed)
		}
	}
	content, failure := os.ReadFile(fixture.Path)
	if failure != nil || string(content) != "first\nsecond\n" {
		t.Fatalf("FIFO file %q: %v", content, failure)
	}
}
