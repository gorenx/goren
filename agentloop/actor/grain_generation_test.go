package loopactor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gorenx/goren/agent"
	middlewareactor "github.com/gorenx/goren/agent/middleware/actor"
	llmactor "github.com/gorenx/goren/llm/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
	"github.com/tochemey/goakt/v4/reentrancy"
)

// silentGeneration accepts generation but withholds its result. The optional
// close reply exercises both confirmed cleanup and unresolved resource release.
type silentGeneration struct {
	closeReply bool
	commands   chan any
	initial    llmactor.Generation
}

func (*silentGeneration) PreStart(*goakt.Context) error { return nil }
func (*silentGeneration) PostStop(*goakt.Context) error { return nil }
func (endpoint *silentGeneration) Receive(rc *goakt.ReceiveContext) {
	envelope, ok := rc.Message().(*goakt.Broadcast)
	if !ok {
		return
	}
	input := envelope.Message()
	endpoint.commands <- input
	switch request := input.(type) {
	case llmactor.Generation:
		endpoint.initial = request
	case llmactor.CloseCall:
		// A result from the expired Generation must not be interpreted as the
		// response to CloseCall, even though both operations share a CallID.
		_ = rc.ActorSystem().TellGrain(rc.Context(), request.ReplyTo, llmactor.GenerationStarted{
			CallID:    request.CallID,
			RequestID: endpoint.initial.RequestID,
		})
		if endpoint.closeReply {
			_ = rc.ActorSystem().TellGrain(rc.Context(), request.ReplyTo, llmactor.CallClosed{
				CallID:    request.CallID,
				RequestID: request.RequestID,
			})
		}
	}
}

func TestModelResultTimeoutClosesWithoutGeneratingAgain(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		confirmed bool
		cancel    bool
	}{
		{
			name:      "close-confirmed",
			confirmed: true,
		},
		{
			name: "close-unconfirmed",
		},
		{
			name:      "cancel-during-close",
			confirmed: true,
		},
		{
			name:      "cancel-acknowledgement-missing",
			confirmed: true,
			cancel:    true,
		},
	} {
		confirmed := scenario.confirmed
		name := scenario.name
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			system, err := goakt.NewActorSystem("model-timeout", goakt.WithLogger(log.DiscardLogger))
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
			storage, err := goakt.GrainOf[*sessionGrain](ctx, system, "storage", goakt.WithLongLivedGrain())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := system.AskGrain(ctx, storage, sessionSetup{history: history}, time.Second); err != nil {
				t.Fatal(err)
			}
			target, err := goakt.GrainOf[*executionGrain](ctx, system, "execution", goakt.WithLongLivedGrain())
			if err != nil {
				t.Fatal(err)
			}
			coordinator, err := goakt.GrainOf[*middlewareactor.Grain](ctx, system, "middleware", goakt.WithLongLivedGrain(), goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
			if err != nil {
				t.Fatal(err)
			}
			commands := make(chan any, 8)
			endpoint, err := system.Spawn(ctx, "generation", &silentGeneration{
				closeReply: confirmed && name != "cancel-during-close",
				commands:   commands,
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = system.AskGrain(ctx, target, executionSetup{
				execute:    true,
				subject:    subject,
				target:     storage,
				middleware: coordinator,
				router:     endpoint,
				timeout:    100 * time.Millisecond,
			}, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			var generated llmactor.Generation
			select {
			case command := <-commands:
				generated = command.(llmactor.Generation)
			case <-time.After(time.Second):
				t.Fatal("generation was not sent")
			}
			if scenario.cancel {
				payload, err := system.AskGrain(ctx, target, Cancel{
					CreationID: "test",
					Cause:      agent.UserCancel{},
				}, time.Second)
				if err != nil {
					t.Fatal(err)
				}
				changed := payload.(Changed)
				if changed.Err == nil || !errors.Is(changed.Err, context.DeadlineExceeded) {
					t.Fatalf("cancel result: %+v", changed)
				}
				select {
				case command := <-commands:
					if _, ok := command.(llmactor.CancelCall); !ok {
						t.Fatalf("cancel command: %T", command)
					}
				case <-time.After(time.Second):
					t.Fatal("cancel was not sent")
				}
			}
			closed := (<-commands).(llmactor.CloseCall)
			if name == "cancel-during-close" {
				// The capability owns Close already; its successful reply can still
				// be in transit after it has removed the generation.
				payload, err := system.AskGrain(ctx, target, Cancel{
					CreationID: "test",
					Cause:      agent.UserCancel{},
				}, time.Second)
				if err != nil || payload.(Changed).Err != nil {
					t.Fatalf("cancel while close pending: %v, %v", payload, err)
				}
				activity, err := system.AskGrain(ctx, target, ReadActivity{
					CreationID: "test",
				}, time.Second)
				if err != nil || activity.(Activity).Status != agent.StatusRunning {
					t.Fatalf("cancel released execution before close acknowledgement: %v, %v", activity, err)
				}
				if err := system.TellGrain(ctx, closed.ReplyTo, llmactor.CallClosed{
					CallID:    closed.CallID,
					RequestID: closed.RequestID,
				}); err != nil {
					t.Fatal(err)
				}
			}
			if generated.CallID != closed.CallID || generated.RequestID == closed.RequestID {
				t.Fatal("cleanup did not retain call identity with a new operation identity")
			}
			select {
			case unexpected := <-commands:
				t.Fatalf("unexpected retry: %T", unexpected)
			default:
			}
			if confirmed {
				payload, err := system.AskGrain(ctx, target, WhenIdle{CreationID: "test"}, 2*time.Second)
				if err != nil || payload.(Completion).Err != nil {
					t.Fatalf("confirmed close did not settle: %v, %v", payload, err)
				}
			} else {
				_, err := system.AskGrain(ctx, target, WhenIdle{CreationID: "test"}, 200*time.Millisecond)
				if err == nil {
					t.Fatal("unconfirmed close released execution")
				}
				payload, err := system.AskGrain(ctx, target, ReadActivity{CreationID: "test"}, time.Second)
				if err != nil || payload.(Activity).Status != agent.StatusRunning {
					t.Fatalf("unconfirmed close lost ownership: %v, %v", payload, err)
				}
			}

		})
	}
}
