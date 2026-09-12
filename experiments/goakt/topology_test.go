package spike

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
)

type topologyProbe struct {
	label   string
	stops   chan string
	failure error
	count   atomic.Int32
}

func (*topologyProbe) PreStart(*goakt.Context) error { return nil }
func (*topologyProbe) Receive(*goakt.ReceiveContext) {}
func (entry *topologyProbe) PostStop(*goakt.Context) error {
	entry.count.Add(1)
	if entry.stops != nil {
		entry.stops <- entry.label
	}
	return entry.failure
}

func TestPhysicalParentAndStopOrder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	system, err := goakt.NewActorSystem("topology", goakt.WithLogger(log.DiscardLogger))
	if err != nil {
		t.Fatal(err)
	}
	if err = system.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = system.Stop(context.Background()) })
	stops := make(chan string, 2)
	parent, err := system.Spawn(ctx, "parent", &topologyProbe{
		label: "parent",
		stops: stops,
	}, goakt.WithLongLived())
	if err != nil {
		t.Fatal(err)
	}
	child, err := parent.SpawnChild(ctx, "child", &topologyProbe{
		label: "child",
		stops: stops,
	}, goakt.WithLongLived())
	if err != nil {
		t.Fatal(err)
	}
	if !child.Parent().Equals(parent) {
		t.Fatal("physical parent mismatch")
	}
	if err = parent.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if child.IsRunning() || parent.IsRunning() {
		t.Fatal("subtree still running")
	}
	if first, second := <-stops, <-stops; first != "child" || second != "parent" {
		t.Fatalf("PostStop order: %s, %s", first, second)
	}
}

func TestPostStopFailureIsNotRetriedByShutdown(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	system, err := goakt.NewActorSystem("failure", goakt.WithLogger(log.DiscardLogger))
	if err != nil {
		t.Fatal(err)
	}
	if err = system.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = system.Stop(context.Background()) })
	entry := &topologyProbe{
		failure: errors.New("injected cleanup failure"),
	}
	endpoint, err := system.Spawn(ctx, "endpoint", entry, goakt.WithLongLived())
	if err != nil {
		t.Fatal(err)
	}
	if err = endpoint.Shutdown(ctx); err == nil {
		t.Fatal("expected cleanup failure")
	}
	if endpoint.IsRunning() {
		t.Fatal("failed shutdown left actor running")
	}
	if err = endpoint.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if calls := entry.count.Load(); calls != 1 {
		t.Fatalf("PostStop calls = %d", calls)
	}
}

type blockingStop struct {
	entered chan struct{}
	release chan struct{}
}

func (*blockingStop) PreStart(*goakt.Context) error { return nil }
func (*blockingStop) Receive(*goakt.ReceiveContext) {}
func (entry *blockingStop) PostStop(*goakt.Context) error {
	close(entry.entered)
	<-entry.release
	return nil
}

func TestParentShutdownWaitsForChildPostStop(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	system, err := goakt.NewActorSystem("blocking", goakt.WithLogger(log.DiscardLogger))
	if err != nil {
		t.Fatal(err)
	}
	if err = system.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = system.Stop(context.Background()) })
	stops := make(chan string, 1)
	parent, err := system.Spawn(ctx, "parent", &topologyProbe{
		label: "parent",
		stops: stops,
	}, goakt.WithLongLived())
	if err != nil {
		t.Fatal(err)
	}
	entry := &blockingStop{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	child, err := parent.SpawnChild(ctx, "child", entry, goakt.WithLongLived())
	if err != nil {
		t.Fatal(err)
	}
	completed := make(chan error, 1)
	go func() { completed <- parent.Shutdown(ctx) }()
	select {
	case <-entry.entered:
	case <-ctx.Done():
		close(entry.release)
		t.Fatal("child PostStop did not start")
	}
	select {
	case <-completed:
		close(entry.release)
		t.Fatal("parent Shutdown returned before child cleanup")
	case <-stops:
		close(entry.release)
		t.Fatal("parent PostStop ran before child cleanup")
	default:
	}
	close(entry.release)
	select {
	case failure := <-completed:
		if failure != nil {
			t.Fatal(failure)
		}
	case <-ctx.Done():
		t.Fatal("parent Shutdown did not complete")
	}
	if parent.IsRunning() || child.IsRunning() {
		t.Fatal("subtree still running")
	}
	if label := <-stops; label != "parent" {
		t.Fatal(label)
	}
}
