package loopactor

import (
	"context"
	"testing"
	"time"

	"github.com/gorenx/goren/agent"
	middlewareactor "github.com/gorenx/goren/agent/middleware/actor"
	"github.com/gorenx/goren/agentmessage"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
	"github.com/tochemey/goakt/v4/reentrancy"
)

func TestStoppingWaitAcceptsSteerAndWaitsForCanceledHook(t *testing.T) {
	for _, scenario := range []string{"steer", "cancel", "retry cancellation"} {
		t.Run(scenario, func(t *testing.T) {
			canceled := scenario != "steer"
			retry := scenario == "retry cancellation"
			ctx := context.Background()
			system, err := goakt.NewActorSystem("stopping", goakt.WithLogger(log.DiscardLogger), goakt.WithPubSub())
			if err != nil {
				t.Fatal(err)
			}
			if err := system.Start(ctx); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := system.Stop(ctx); err != nil {
					t.Error(err)
				}
			})
			subject, history := acceptedTurn(t)
			backend := &modelProvider{}
			pool := modelPool(t, system, backend)
			storage, err := goakt.GrainOf[*sessionGrain](ctx, system, "storage", goakt.WithLongLivedGrain())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := system.AskGrain(ctx, storage, sessionSetup{
				history: history,
			}, time.Second); err != nil {
				t.Fatal(err)
			}
			target, err := goakt.GrainOf[*executionGrain](ctx, system, "execution", goakt.WithLongLivedGrain())
			if err != nil {
				t.Fatal(err)
			}
			hooks, err := goakt.GrainOf[*stoppingEndpoint](ctx, system, "hooks", goakt.WithLongLivedGrain())
			if err != nil {
				t.Fatal(err)
			}
			entered := make(chan struct{})
			if _, err := system.AskGrain(ctx, hooks, stoppingSetup{
				entered:    entered,
				failCancel: retry,
			}, time.Second); err != nil {
				t.Fatal(err)
			}
			coordinator, err := goakt.GrainOf[*middlewareactor.Grain](ctx, system, "coordinator",
				goakt.WithLongLivedGrain(),
				goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))),
			)
			if err != nil {
				t.Fatal(err)
			}
			_, err = system.AskGrain(ctx, target, executionSetup{
				execute:    true,
				subject:    subject,
				target:     storage,
				middleware: coordinator,
				router:     pool,
				timeout:    time.Second,
				handlers: middlewareactor.Handlers{
					TurnStopping: []middlewareactor.Route{
						{
							Definition: "stopping",
							Target:     hooks,
						},
					},
				},
			}, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("hook did not start")
			}
			message, err := agentmessage.NewUserMessage(agentmessage.UserMessageInput{
				Content: []agentmessage.ContentBlock{agentmessage.NewTextBlock("steer during hook")},
				Source:  agentmessage.UserMessageSource{},
			})
			if err != nil {
				t.Fatal(err)
			}
			payload, err := system.AskGrain(ctx, target, Steer{
				CreationID: "test",
				Message:    message,
			}, time.Second)
			if err != nil || payload.(Changed).Err != nil {
				t.Fatalf("Steer blocked: %v, %v", payload, err)
			}
			if canceled {
				payload, err = system.AskGrain(ctx, target, Cancel{
					CreationID: "test",
					Cause:      agent.UserCancel{},
					Options: agent.CancelOptions{
						KeepInbox: true,
					},
				}, time.Second)
				if err != nil || (payload.(Changed).Err != nil) != retry {
					t.Fatalf("Cancel acknowledgement: %v, %v", payload, err)
				}
				if retry {
					payload, err = system.AskGrain(ctx, target, Cancel{
						CreationID: "test",
						Cause:      agent.ParentCancel{},
						Options: agent.CancelOptions{
							KeepInbox: true,
						},
					}, time.Second)
					if err != nil || payload.(Changed).Err != nil {
						t.Fatalf("Cancel retry: %v, %v", payload, err)
					}
				}
			}
			payload, err = system.AskGrain(ctx, target, ReadActivity{CreationID: "test"}, time.Second)
			if err != nil || payload.(Activity).Status != agent.StatusRunning {
				t.Fatalf("hook prematurely settled execution: %v, %v", payload, err)
			}
			if _, err := system.AskGrain(ctx, hooks, releaseStopping{}, time.Second); err != nil {
				t.Fatal(err)
			}
			payload, err = system.AskGrain(ctx, target, WhenIdle{CreationID: "test"}, 3*time.Second)
			if err != nil || payload.(Completion).Err != nil {
				t.Fatalf("hook did not settle: %v, %v", payload, err)
			}
			expected := int32(2)
			if canceled {
				expected = 1
			}
			if backend.calls.Load() != expected {
				t.Fatalf("model calls = %d, want %d", backend.calls.Load(), expected)
			}

		})
	}
}
