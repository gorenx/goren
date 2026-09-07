package spike

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tochemey/goakt/v4/actor"
)

type deactivationConfigure struct {
	Entered chan<- struct{}
	Release <-chan struct{}
	Fail    bool
}

// DeactivationGrain makes the hook interval observable. The gate belongs to
// the test driver; production cleanup must drain effects before this hook.
type DeactivationGrain struct {
	entered chan<- struct{}
	release <-chan struct{}
	fail    bool
}

func (*DeactivationGrain) OnActivate(context.Context, *actor.GrainProps) error {
	return nil
}

func (owner *DeactivationGrain) OnDeactivate(stopContext context.Context, _ *actor.GrainProps) error {
	if owner.entered == nil {
		return nil
	}
	owner.entered <- struct{}{}
	select {
	case <-owner.release:
	case <-stopContext.Done():
		return stopContext.Err()
	}
	if owner.fail {
		return errors.New("deactivation-fixture-failed")
	}
	return nil
}

func (owner *DeactivationGrain) OnReceive(gc *actor.GrainContext) {
	switch incoming := gc.Message().(type) {
	case deactivationConfigure:
		owner.entered = incoming.Entered
		owner.release = incoming.Release
		owner.fail = incoming.Fail
		gc.Response(true)
	}
}

func TestGrainStopAcknowledgesHookCompletionAndFailure(t *testing.T) {
	for _, failHook := range []bool{false, true} {
		t.Run(map[bool]string{
			false: "success",
			true:  "failure",
		}[failHook], func(t *testing.T) {
			fixture := newRequestHarness(t)
			identity, failure := actor.GrainOf[*DeactivationGrain](context.Background(), fixture.system, "deactivation",
				actor.WithLongLivedGrain())
			if failure != nil {
				t.Fatal(failure)
			}
			entered := make(chan struct{}, 2)
			gate := make(chan struct{})
			release := sync.OnceFunc(func() { close(gate) })
			t.Cleanup(release)
			inspectGrain(t, fixture.system, identity, deactivationConfigure{
				Entered: entered,
				Release: gate,
				Fail:    failHook,
			})
			completed := make(chan error, 1)
			stopContext, cancelStop := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancelStop()
			// Only the external driver waits on the framework stop call.
			go func() {
				completed <- fixture.system.TellGrain(stopContext, identity, new(actor.PoisonPill))
			}()
			awaitSignal(t, entered)
			select {
			case result := <-completed:
				t.Fatalf("stop returned before hook finished: %v", result)
			default:
			}
			release()
			select {
			case result := <-completed:
				if failHook {
					if result == nil || !strings.Contains(result.Error(), "deactivation-fixture-failed") {
						t.Fatalf("hook failure lost: %v", result)
					}
				} else if result != nil {
					t.Fatal(result)
				}
			case <-stopContext.Done():
				t.Fatal("stop failed to return after hook exit")
			}
		})
	}
}
