package loopactor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gorenx/goren/agentloop"
	stepstate "github.com/gorenx/goren/agentloop/internal/step"
	"github.com/gorenx/goren/session"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
	"github.com/tochemey/goakt/v4/reentrancy"
)

type stepSettlementCaller struct {
	target  *goakt.GrainIdentity
	replies chan stepFinished
}

func (*stepSettlementCaller) PreStart(*goakt.Context) error { return nil }
func (*stepSettlementCaller) PostStop(*goakt.Context) error { return nil }
func (driver *stepSettlementCaller) Receive(rc *goakt.ReceiveContext) {
	input, ok := rc.Message().(finishStep)
	if !ok {
		return
	}
	replies := driver.replies
	rc.RequestGrain(driver.target, input).Then(func(payload any, failure error) {
		result, valid := payload.(stepFinished)
		if failure != nil {
			result.err = failure
		} else if !valid {
			result.err = errors.New("unexpected Step settlement response")
		}
		replies <- result
	})
}

func TestStepSettlementRetriesLostAcknowledgementWithoutDuplicateEnd(t *testing.T) {
	ctx := context.Background()
	system, err := goakt.NewActorSystem("step-settlement", goakt.WithLogger(log.DiscardLogger))
	if err != nil {
		t.Fatal(err)
	}
	err = system.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	stopped := false
	t.Cleanup(func() {
		if stopped {
			return
		}
		failure := system.Stop(ctx)
		if failure != nil {
			t.Error(failure)
		}
	})
	subject, history := preparedTurn(t)
	prepareRequestStep(t, subject, history)
	ending, err := subject.PrepareStepEnd(stepstate.StepResultError)
	if err != nil {
		t.Fatal(err)
	}
	ending.Err = errors.New("provider failed")
	storage, err := goakt.GrainOf[*settlementStorage](ctx, system, "storage", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	_, err = system.AskGrain(ctx, storage, storageSetup{
		history: history,
		failure: "commit reply",
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	target, err := goakt.GrainOf[*settlementAgent](ctx, system, "execution", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	_, err = system.AskGrain(ctx, target, settlementSetup{
		subject: subject,
		target:  storage,
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	replies := make(chan stepFinished, 1)
	driver, err := system.Spawn(ctx, "caller", &stepSettlementCaller{
		target:  target,
		replies: replies,
	}, goakt.WithReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		err = goakt.Tell(ctx, driver, finishStep{ending: ending})
		if err != nil {
			t.Fatal(err)
		}
		select {
		case result := <-replies:
			if (result.err != nil) != (attempt == 0) {
				t.Fatalf("attempt %d: %v", attempt, result.err)
			}
			if !errors.Is(result.failure, ending.Err) {
				t.Fatalf("execution error lost: %v", result.failure)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("Step settlement timed out")
		}
	}
	// Stop transfers the fixture's mutable business objects back to this test.
	err = system.Stop(ctx)
	if err != nil {
		t.Fatal(err)
	}
	stopped = true
	ends := 0
	for _, entry := range history.Events() {
		if entry.Type == session.StepEndEventName {
			ends++
		}
	}
	if ends != 1 {
		t.Fatalf("Step ends = %d", ends)
	}
	next, err := subject.ContinueTurn(ending.Err)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := next.(agentloop.TurnEnd); !ok {
		t.Fatalf("next = %T", next)
	}
}
