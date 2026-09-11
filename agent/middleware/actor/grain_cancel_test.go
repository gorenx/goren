package middlewareactor_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gorenx/goren/agent"
	middlewareactor "github.com/gorenx/goren/agent/middleware/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
	"github.com/tochemey/goakt/v4/reentrancy"
)

type setup struct {
	leaf     bool
	entered  chan struct{}
	canceled chan error
	returned chan struct{}
}
type release struct {
	invalid bool
}

// handler holds an Invoke open. An outer handler awaits Continue, while the
// leaf waits for an explicit test release, including after cancellation.
type handler struct {
	settings setup
	reply    *goakt.GrainReply
}

func (*handler) OnActivate(context.Context, *goakt.GrainProps) error   { return nil }
func (*handler) OnDeactivate(context.Context, *goakt.GrainProps) error { return nil }
func (owner *handler) OnReceive(gc *goakt.GrainContext) {
	switch input := gc.Message().(type) {
	case setup:
		owner.settings = input
		gc.Response(struct{}{})
	case middlewareactor.InvokePreStep:
		reply := gc.DeferResponse()
		if owner.settings.leaf {
			owner.reply = reply
			close(owner.settings.entered)
			return
		}
		returned := owner.settings.returned
		gc.RequestGrain(input.Invocation.Coordinator, middlewareactor.ContinuePreStep{
			CallID:  input.Invocation.CallID,
			Handler: input.Invocation.Handler,
		}, goakt.WithReentrancyMode(reentrancy.AllowAll)).Then(func(payload any, failure error) {
			result, ok := payload.(agent.PreStepResolved)
			if failure != nil {
				result.Err = failure
			} else if !ok {
				result.Err = errors.New("test: invalid downstream result")
			}
			close(returned)
			reply.Response(result)
		})
	case middlewareactor.CancelInvocation:
		owner.settings.canceled <- input.Cause
		gc.Response(middlewareactor.Completion{})
	case release:
		if input.invalid {
			owner.reply.Response("invalid handler reply")
		} else {
			owner.reply.Response(agent.PreStepResolved{
				Decision: agent.PreStepDecision{
					Kind: agent.PreStepEnter,
				},
			})
		}
		owner.reply = nil
		gc.Response(struct{}{})
	}
}

type resolution struct {
	result agent.PreStepResolved
	err    error
}

func TestAroundCancellationWaitsForDownstreamAndUnwinds(t *testing.T) {
	for _, scenario := range []string{"normal", "cancel", "invalid leaf reply"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			system, err := goakt.NewActorSystem("around", goakt.WithLogger(log.DiscardLogger))
			if err != nil {
				t.Fatal(err)
			}
			if err := system.Start(ctx); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := system.Stop(context.Background()); err != nil {
					t.Error(err)
				}
			})
			mode := goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll)))
			coordinator, err := goakt.GrainOf[*middlewareactor.Grain](ctx, system, "coordinator", goakt.WithLongLivedGrain(), mode)
			if err != nil {
				t.Fatal(err)
			}
			for _, input := range []middlewareactor.CancelCall{
				{
					Cause: context.Canceled,
				},
				{
					CallID: "missing-cause",
				},
			} {
				payload, failure := system.AskGrain(ctx, coordinator, input, time.Second)
				if failure != nil {
					t.Fatal(failure)
				}
				ack, valid := payload.(middlewareactor.Completion)
				if !valid || ack.Err == nil {
					t.Fatalf("invalid cancellation accepted: %+v", payload)
				}
			}
			outer, err := goakt.GrainOf[*handler](ctx, system, "outer", goakt.WithLongLivedGrain(), mode)
			if err != nil {
				t.Fatal(err)
			}
			inner, err := goakt.GrainOf[*handler](ctx, system, "inner", goakt.WithLongLivedGrain(), mode)
			if err != nil {
				t.Fatal(err)
			}
			entered := make(chan struct{})
			returned := make(chan struct{})
			outerCanceled := make(chan error, 2)
			innerCanceled := make(chan error, 2)
			if _, err := system.AskGrain(ctx, outer, setup{
				canceled: outerCanceled,
				returned: returned,
			}, time.Second); err != nil {
				t.Fatal(err)
			}
			if _, err := system.AskGrain(ctx, inner, setup{
				leaf:     true,
				entered:  entered,
				canceled: innerCanceled,
			}, time.Second); err != nil {
				t.Fatal(err)
			}
			completed := make(chan resolution, 1)
			go func() {
				payload, failure := system.AskGrain(ctx, coordinator, middlewareactor.ResolvePreStep{
					CallID: "chain",
					Default: agent.PreStepDecision{
						Kind: agent.PreStepEnter,
					},
					Routes: []middlewareactor.Route{
						{
							Definition: "outer",
							Target:     outer,
						},
						{
							Definition: "inner",
							Target:     inner,
						},
					},
					Input: agent.PreStepInput{
						AgentID: "agent",
						Turn:    1,
						Step:    1,
					},
				}, 4*time.Second)
				result, ok := payload.(agent.PreStepResolved)
				if failure == nil && !ok {
					failure = errors.New("invalid root reply")
				}
				completed <- resolution{
					result: result,
					err:    failure,
				}
			}()
			select {
			case <-entered:
			case result := <-completed:
				t.Fatalf("root rejected before inner invocation: %v, %v", result.result.Err, result.err)
			case <-ctx.Done():
				t.Fatal("inner handler did not start")
			}
			if scenario == "cancel" {
				payload, err := system.AskGrain(ctx, coordinator, middlewareactor.CancelCall{
					CallID: "chain",
					Cause:  agent.NewCancellation(agent.ParentCancel{}),
				}, time.Second)
				if err != nil || payload.(middlewareactor.Completion).Err != nil {
					t.Fatalf("cancel: %v, %v", payload, err)
				}
				for _, notified := range []<-chan error{outerCanceled, innerCanceled} {
					select {
					case cause := <-notified:
						var intent agent.Cancellation
						if !errors.As(cause, &intent) || intent.Cause.CancelKind() != "parent" {
							t.Fatalf("wrong cause: %v", cause)
						}
					case <-ctx.Done():
						t.Fatal("handler was not canceled")
					}
				}
			}
			select {
			case result := <-completed:
				t.Fatalf("root finished before leaf: %+v", result)
			case <-returned:
				t.Fatal("Continue returned before leaf")
			default:
			}
			if _, err := system.AskGrain(ctx, inner, release{
				invalid: scenario == "invalid leaf reply",
			}, time.Second); err != nil {
				t.Fatal(err)
			}
			select {
			case result := <-completed:
				if result.err != nil {
					t.Fatal(result.err)
				}
				if (result.result.Err != nil) != (scenario != "normal") {
					t.Fatalf("root error: %v", result.result.Err)
				}
				if scenario == "cancel" {
					var intent agent.Cancellation
					if !errors.As(result.result.Err, &intent) {
						t.Fatalf("lost cancellation: %v", result.result.Err)
					}
				}
			case <-ctx.Done():
				t.Fatal("around chain failed to unwind")
			}
			select {
			case <-returned:
			default:
				t.Fatal("root completed before outer handler")
			}
			// The coordinator has completed the original request. Its caller may
			// still have a cancellation in flight until it consumes that reply.
			for attempt := 0; attempt < 2; attempt++ {
				payload, failure := system.AskGrain(ctx, coordinator, middlewareactor.CancelCall{
					CallID: "chain",
					Cause:  agent.NewCancellation(agent.ParentCancel{}),
				}, time.Second)
				if failure != nil {
					t.Fatal(failure)
				}
				ack, valid := payload.(middlewareactor.Completion)
				if !valid || ack.Err != nil {
					t.Fatalf("late cancellation %d: %+v", attempt, payload)
				}
			}
		})
	}
}
