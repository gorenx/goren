// This isolated module tests a candidate protocol, not the production Goren runtime.
package spike

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
)

type begin struct {
	Release <-chan struct{}
}
type cancelTurn struct{}
type inspect struct{}
type view struct {
	Generation uint64
	Phase      string
	Outcome    string
	Discarded  int
}
type appendRecord struct {
	Generation uint64
	Kind       string
	ReplyTo    *actor.PID
}
type appended struct {
	Generation uint64
	Kind       string
	Err        error
}
type storageDone struct {
	Request appendRecord
	Err     error
}
type taskDone struct {
	Generation uint64
	Err        error
}
type recordCommitted struct {
	Sequence int
	Kind     string
}

type fixtureSubscriber struct {
	ready   chan<- struct{}
	notices chan<- recordCommitted
}

func (*fixtureSubscriber) PreStart(*actor.Context) error { return nil }
func (*fixtureSubscriber) PostStop(*actor.Context) error { return nil }
func (owner *fixtureSubscriber) Receive(rc *actor.ReceiveContext) {
	switch incoming := rc.Message().(type) {
	case *actor.PostStart:
		rc.Tell(rc.ActorSystem().TopicActor(), actor.NewSubscribe("session-events"))
	case *actor.SubscribeAck:
		owner.ready <- struct{}{}
	case recordCommitted:
		owner.notices <- incoming
	}
}

// fixtureSession separates accepting a write from confirming its commit.
// The gate simulates storage latency; the records are memory-only, not durable.
type fixtureSession struct {
	gate    <-chan struct{}
	failure error
	busy    bool
	records []string
}

func (*fixtureSession) PreStart(*actor.Context) error { return nil }
func (*fixtureSession) PostStop(*actor.Context) error { return nil }
func (owner *fixtureSession) Receive(rc *actor.ReceiveContext) {
	switch incoming := rc.Message().(type) {
	case appendRecord:
		if owner.busy {
			rc.Tell(incoming.ReplyTo, appended{
				Generation: incoming.Generation,
				Kind:       incoming.Kind,
				Err:        errors.New("fixture only supports one pending write"),
			})
			return
		}
		owner.busy = true
		gate, failure := owner.gate, owner.failure
		rc.PipeTo(rc.Self(), func() (any, error) {
			<-gate
			return storageDone{
				Request: incoming,
				Err:     failure,
			}, nil
		})
	case storageDone:
		owner.busy = false
		if incoming.Err == nil {
			owner.records = append(owner.records, incoming.Request.Kind)
			rc.Tell(rc.ActorSystem().TopicActor(), actor.NewPublish(
				strconv.Itoa(len(owner.records)), "session-events", recordCommitted{
					Sequence: len(owner.records),
					Kind:     incoming.Request.Kind,
				},
			))
		}
		rc.Tell(incoming.Request.ReplyTo, appended{
			Generation: incoming.Request.Generation,
			Kind:       incoming.Request.Kind,
			Err:        incoming.Err,
		})
	case inspect:
		rc.Response(append([]string(nil), owner.records...))
	}
}

// fixtureAgent owns only execution state. Workers capture inputs and report
// immutable completion messages; they never access its state or ReceiveContext.
type fixtureAgent struct {
	journal     *actor.PID
	release     <-chan struct{}
	entered     chan<- struct{}
	cancelSeen  chan<- struct{}
	cooperative bool
	workers     sync.WaitGroup
	stopTask    context.CancelFunc
	state       view
}

func (owner *fixtureAgent) PreStart(*actor.Context) error {
	owner.state.Phase = "idle"
	return nil
}

func (owner *fixtureAgent) PostStop(*actor.Context) error {
	if owner.stopTask != nil {
		owner.stopTask()
	}
	owner.workers.Wait()
	return nil
}

func (owner *fixtureAgent) Receive(rc *actor.ReceiveContext) {
	switch incoming := rc.Message().(type) {
	case begin:
		if owner.state.Phase != "idle" {
			rc.Response(false)
			return
		}
		owner.state.Generation++
		if incoming.Release != nil {
			owner.release = incoming.Release
		}
		owner.state.Phase = "admitting"
		owner.state.Outcome = ""
		rc.Tell(owner.journal, appendRecord{
			Generation: owner.state.Generation,
			Kind:       "input",
			ReplyTo:    rc.Self(),
		})
		rc.Response(true) // Admission to this actor only, not Session commit.
	case cancelTurn:
		switch owner.state.Phase {
		case "admitting":
			owner.state.Phase = "cancel-admitting"
		case "running":
			owner.state.Phase = "cancelling"
			owner.stopTask()
		}
		rc.Response(owner.state)
	case appended:
		if incoming.Generation != owner.state.Generation {
			owner.state.Discarded++
			return
		}
		if incoming.Err != nil {
			owner.state.Phase, owner.state.Outcome = "idle", "commit-failed"
			return
		}
		if incoming.Kind == "input" {
			if owner.state.Phase == "cancel-admitting" {
				owner.state.Phase, owner.state.Outcome = "idle", "cancelled"
				return
			}
			owner.launch(rc)
		} else {
			owner.state.Phase, owner.state.Outcome = "idle", "completed"
		}
	case taskDone:
		if incoming.Generation != owner.state.Generation ||
			(owner.state.Phase != "running" && owner.state.Phase != "cancelling") {
			owner.state.Discarded++
			return
		}
		owner.stopTask()
		if owner.state.Phase == "cancelling" || incoming.Err != nil {
			owner.state.Phase, owner.state.Outcome = "idle", "cancelled"
			return
		}
		owner.state.Phase = "committing"
		rc.Tell(owner.journal, appendRecord{
			Generation: incoming.Generation,
			Kind:       "result",
			ReplyTo:    rc.Self(),
		})
	case inspect:
		rc.Response(owner.state)
	}
}

func (owner *fixtureAgent) launch(rc *actor.ReceiveContext) {
	owner.state.Phase = "running"
	operationContext, cancel := context.WithCancel(context.Background())
	owner.stopTask = cancel
	generation := owner.state.Generation
	release, entered, cancelSeen := owner.release, owner.entered, owner.cancelSeen
	cooperative := owner.cooperative
	joined := &owner.workers
	joined.Add(1)
	rc.PipeTo(rc.Self(), func() (any, error) {
		defer joined.Done()
		entered <- struct{}{}
		select {
		case <-release:
		case <-operationContext.Done():
			cancelSeen <- struct{}{}
			if !cooperative {
				<-release // Simulate an external effect that cannot be interrupted.
			}
		}
		return taskDone{
			Generation: generation,
			Err:        operationContext.Err(),
		}, nil
	})
}

func ask(t *testing.T, target *actor.PID, message any) any {
	t.Helper()
	response, err := actor.Ask(context.Background(), target, message, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func awaitSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(3 * time.Second):
		t.Fatal("signal timeout")
	}
}

func awaitState(t *testing.T, target *actor.PID, phase, outcome string) view {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		observed := ask(t, target, inspect{}).(view)
		if observed.Phase == phase && observed.Outcome == outcome {
			return observed
		}
		if time.Now().After(deadline) {
			t.Fatalf("wanted %s/%s; got %+v", phase, outcome, observed)
		}
		time.Sleep(time.Millisecond)
	}
}

func setup(t *testing.T, fail error, cooperative bool) (*actor.PID, *actor.PID, func(), func(), <-chan struct{}, <-chan struct{}) {
	t.Helper()
	system, err := actor.NewActorSystem("spike", actor.WithLogger(log.DiscardLogger), actor.WithPubSub())
	if err != nil {
		t.Fatal(err)
	}
	if err = system.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := system.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})
	commitGate, taskGate := make(chan struct{}), make(chan struct{})
	openCommit := sync.OnceFunc(func() { close(commitGate) })
	openTask := sync.OnceFunc(func() { close(taskGate) })
	t.Cleanup(openCommit)
	t.Cleanup(openTask)
	entered, cancelled := make(chan struct{}, 8), make(chan struct{}, 8)
	journal, err := system.Spawn(context.Background(), "session", &fixtureSession{
		gate:    commitGate,
		failure: fail,
	}, actor.WithLongLived())
	if err != nil {
		t.Fatal(err)
	}
	subject, err := system.Spawn(context.Background(), "agent", &fixtureAgent{
		journal:     journal,
		release:     taskGate,
		entered:     entered,
		cancelSeen:  cancelled,
		cooperative: cooperative,
	}, actor.WithLongLived())
	if err != nil {
		t.Fatal(err)
	}
	return subject, journal, openCommit, openTask, entered, cancelled
}

func TestCancelWhileCommitPendingDoesNotPretendRollback(t *testing.T) {
	subject, journal, openCommit, _, entered, _ := setup(t, nil, true)
	if ask(t, subject, begin{}) != true {
		t.Fatal("start rejected")
	}
	if observed := ask(t, subject, cancelTurn{}).(view); observed.Phase != "cancel-admitting" {
		t.Fatalf("cancel blocked or lost: %+v", observed)
	}
	if records := ask(t, journal, inspect{}).([]string); len(records) != 0 {
		t.Fatal("write visible before commit")
	}
	openCommit()
	awaitState(t, subject, "idle", "cancelled")
	if records := ask(t, journal, inspect{}).([]string); !reflect.DeepEqual(records, []string{"input"}) {
		t.Fatalf("committed input lost: %v", records)
	}
	select {
	case <-entered:
		t.Fatal("cancelled admission launched worker")
	default:
	}
}

func TestCancelJoinsWorkerAndRejectsLateGeneration(t *testing.T) {
	subject, journal, openCommit, openTask, entered, cancelled := setup(t, nil, false)
	openCommit()
	ask(t, subject, begin{})
	awaitSignal(t, entered)
	previous := ask(t, subject, cancelTurn{}).(view)
	awaitSignal(t, cancelled)
	if observed := ask(t, subject, inspect{}).(view); observed.Phase != "cancelling" {
		t.Fatalf("claimed idle before external effect settled: %+v", observed)
	}
	if ask(t, subject, begin{}) != false {
		t.Fatal("accepted successor before worker settled")
	}
	openTask()
	awaitState(t, subject, "idle", "cancelled")
	successorGate := make(chan struct{})
	openSuccessor := sync.OnceFunc(func() { close(successorGate) })
	t.Cleanup(openSuccessor)
	ask(t, subject, begin{
		Release: successorGate,
	})
	awaitSignal(t, entered)
	if err := actor.Tell(context.Background(), subject, taskDone{
		Generation: previous.Generation,
	}); err != nil {
		t.Fatal(err)
	}
	observed := ask(t, subject, inspect{}).(view)
	if observed.Discarded != 1 || observed.Phase != "running" {
		t.Fatalf("late result changed successor: %+v", observed)
	}
	if records := ask(t, journal, inspect{}).([]string); !reflect.DeepEqual(records, []string{"input", "input"}) {
		t.Fatalf("late result entered history: %v", records)
	}
	openSuccessor()
	awaitState(t, subject, "idle", "completed")
	if records := ask(t, journal, inspect{}).([]string); !reflect.DeepEqual(records, []string{"input", "input", "result"}) {
		t.Fatalf("unexpected history: %v", records)
	}
}

func TestCommitFailureDoesNotLaunchWorker(t *testing.T) {
	subject, journal, openCommit, _, entered, _ := setup(t, errors.New("storage failure"), true)
	openCommit()
	ask(t, subject, begin{})
	awaitState(t, subject, "idle", "commit-failed")
	if records := ask(t, journal, inspect{}).([]string); len(records) != 0 {
		t.Fatal("failed write entered history")
	}
	select {
	case <-entered:
		t.Fatal("failed commit launched worker")
	default:
	}
}

func TestShutdownCancelsAndJoinsCooperativeWorker(t *testing.T) {
	subject, _, openCommit, _, entered, cancelled := setup(t, nil, true)
	openCommit()
	ask(t, subject, begin{})
	awaitSignal(t, entered)
	if err := subject.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	awaitSignal(t, cancelled)
	if subject.IsRunning() {
		t.Fatal("actor still running after shutdown")
	}
}

func TestStandalonePubSubFansOutCommittedFacts(t *testing.T) {
	subject, journal, openCommit, _, entered, _ := setup(t, nil, true)
	ready := make(chan struct{}, 2)
	feeds := []chan recordCommitted{make(chan recordCommitted, 4), make(chan recordCommitted, 4)}
	for index, feed := range feeds {
		_, err := subject.ActorSystem().Spawn(context.Background(), "observer"+strconv.Itoa(index), &fixtureSubscriber{
			ready:   ready,
			notices: feed,
		}, actor.WithLongLived())
		if err != nil {
			t.Fatal(err)
		}
	}
	awaitSignal(t, ready)
	awaitSignal(t, ready)
	ask(t, subject, begin{})
	if records := ask(t, journal, inspect{}).([]string); len(records) != 0 {
		t.Fatal("uncommitted input visible")
	}
	for _, feed := range feeds {
		select {
		case <-feed:
			t.Fatal("fact published before commit")
		default:
		}
	}
	openCommit()
	awaitSignal(t, entered)
	for _, feed := range feeds {
		select {
		case notice := <-feed:
			if notice.Sequence != 1 || notice.Kind != "input" {
				t.Fatalf("unexpected fact: %+v", notice)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("subscriber missed committed fact")
		}
	}
	ask(t, subject, cancelTurn{})
	awaitState(t, subject, "idle", "cancelled")
}
