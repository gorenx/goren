package loopactor

import (
	"context"
	"strings"
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
	initial    llmactor.Generate
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
	case llmactor.Generate:
		endpoint.initial = request
	case llmactor.CloseCall:
		// A result from the expired Generate must not be interpreted as the
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
			subject, history := preparedTurn(t)
			prepareRequestStep(t, subject, history)
			storage, err := goakt.GrainOf[*settlementStorage](ctx, system, "storage", goakt.WithLongLivedGrain())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := system.AskGrain(ctx, storage, storageSetup{history: history}, time.Second); err != nil {
				t.Fatal(err)
			}
			target, err := goakt.GrainOf[*settlementAgent](ctx, system, "execution", goakt.WithLongLivedGrain())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := system.AskGrain(ctx, target, settlementSetup{
				subject: subject,
				target:  storage,
			}, time.Second); err != nil {
				t.Fatal(err)
			}
			settings, err := goakt.GrainOf[*llmactor.Grain](ctx, system, "settings", goakt.WithLongLivedGrain())
			if err != nil {
				t.Fatal(err)
			}
			hooks, err := goakt.GrainOf[*modelHooks](ctx, system, "hooks", goakt.WithLongLivedGrain())
			if err != nil {
				t.Fatal(err)
			}
			coordinator, err := goakt.GrainOf[*middlewareactor.Grain](ctx, system, "middleware", goakt.WithLongLivedGrain(), goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
			if err != nil {
				t.Fatal(err)
			}
			commands := make(chan any, 8)
			endpoint, err := system.Spawn(ctx, "generation", &silentGeneration{
				closeReply: confirmed,
				commands:   commands,
			})
			if err != nil {
				t.Fatal(err)
			}
			responses := make(chan responseResult, 1)
			go func() {
				payload, failure := system.AskGrain(ctx, target, requestModel{
					configuration: settings,
					router:        endpoint,
					middleware:    coordinator,
					replyTimeout:  40 * time.Millisecond,
					requestRoutes: []middlewareactor.Route{{
						Definition: "configure",
						Target:     hooks,
					}},
				}, 3*time.Second)
				responses <- responseResult{
					payload: payload,
					err:     failure,
				}
			}()
			var generated llmactor.Generate
			select {
			case command := <-commands:
				generated = command.(llmactor.Generate)
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
				if changed.Err == nil || !strings.Contains(changed.Err.Error(), "acknowledgement timed out") {
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
			completed := <-responses
			if completed.err != nil {
				t.Fatal(completed.err)
			}
			result := completed.payload.(modelResult)
			if result.err == nil || !strings.Contains(result.err.Error(), "result timed out") {
				t.Fatalf("outcome: %+v", result)
			}
			closed := (<-commands).(llmactor.CloseCall)
			if generated.CallID != closed.CallID || generated.RequestID == closed.RequestID {
				t.Fatal("cleanup did not retain call identity with a new operation identity")
			}
			select {
			case unexpected := <-commands:
				t.Fatalf("unexpected retry: %T", unexpected)
			default:
			}
			if confirmed {
				if _, err := subject.CompleteModel(); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := subject.CompleteModel(); err == nil {
					t.Fatal("unconfirmed closure allowed Step continuation")
				}
			}
		})
	}
}

type responseResult struct {
	payload any
	err     error
}
