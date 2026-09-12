package loopactor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gorenx/goren/agent"
	agentactor "github.com/gorenx/goren/agent/actor"
	middlewareactor "github.com/gorenx/goren/agent/middleware/actor"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
	"github.com/tochemey/goakt/v4/reentrancy"
)

// closingProbe signals mailbox acceptance while using production close handling.
type closingProbe struct {
	executionGrain
	accepted chan struct{}
}

type observeClosing struct{ accepted chan struct{} }

func (owner *closingProbe) OnReceive(gc *goakt.GrainContext) {
	if input, ok := gc.Message().(observeClosing); ok {
		owner.accepted = input.accepted
		gc.Response(struct{}{})
		return
	}
	_, closing := gc.Message().(agentactor.PrepareClose)
	owner.executionGrain.OnReceive(gc)
	if closing {
		owner.accepted <- struct{}{}
	}
}

type finishingModel struct {
	modelProvider
	entered  chan struct{}
	release  chan struct{}
	canceled chan struct{}
}

func (backend *finishingModel) Stream(ctx context.Context, input llm.GenerateOptions) (llm.ChunkStream, error) {
	close(backend.entered)
	select {
	case <-ctx.Done():
		close(backend.canceled)
		return nil, context.Cause(ctx)
	case <-backend.release:
		return backend.modelProvider.Stream(ctx, input)
	}
}

func TestPrepareCloseFinishesCurrentWorkAndAllowsExplicitCancellation(t *testing.T) {
	for _, cancelWork := range []bool{false, true} {
		label := "natural completion"
		if cancelWork {
			label = "parent cancellation"
		}
		t.Run(label, func(t *testing.T) {
			ctx := context.Background()
			system, err := goakt.NewActorSystem("drain", goakt.WithLogger(log.DiscardLogger), goakt.WithPubSub())
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
			if _, err := system.AskGrain(ctx, storage, sessionSetup{
				history: history,
			}, time.Second); err != nil {
				t.Fatal(err)
			}
			target, err := goakt.GrainOf[*closingProbe](ctx, system, "execution", goakt.WithLongLivedGrain(), goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
			if err != nil {
				t.Fatal(err)
			}
			accepted := make(chan struct{}, 4)
			if _, err := system.AskGrain(ctx, target, observeClosing{
				accepted: accepted,
			}, time.Second); err != nil {
				t.Fatal(err)
			}
			backend := &finishingModel{
				entered:  make(chan struct{}),
				release:  make(chan struct{}),
				canceled: make(chan struct{}),
			}
			// Ensure any failed test releases the model before ActorSystem cleanup.
			defer func() {
				select {
				case <-backend.release:
				default:
					close(backend.release)
				}
			}()
			pool := modelPool(t, system, backend)
			coordinator, err := goakt.GrainOf[*middlewareactor.Grain](ctx, system, "middleware", goakt.WithLongLivedGrain(), goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := system.AskGrain(ctx, target, executionSetup{
				execute:    true,
				subject:    subject,
				target:     storage,
				router:     pool,
				middleware: coordinator,
				timeout:    5 * time.Second,
			}, time.Second); err != nil {
				t.Fatal(err)
			}
			select {
			case <-backend.entered:
			case <-time.After(3 * time.Second):
				t.Fatal("model not started")
			}
			queued, err := agentmessage.NewUserMessage(agentmessage.UserMessageInput{
				Content: []agentmessage.ContentBlock{agentmessage.NewTextBlock("next task")},
				Source:  agentmessage.UserMessageSource{},
			})
			if err != nil {
				t.Fatal(err)
			}
			payload, err := system.AskGrain(ctx, target, Followup{
				CreationID: "test",
				Message:    queued,
			}, time.Second)
			if err != nil || payload.(Changed).Err != nil {
				t.Fatalf("queue input: %v %v", payload, err)
			}
			payload, err = system.AskGrain(ctx, target, agentactor.PrepareClose{
				CreationID:    "stale",
				FinishCurrent: true,
			}, time.Second)
			if err != nil || payload.(agentactor.Prepared).Err == nil {
				t.Fatalf("old creation: %v %v", payload, err)
			}
			<-accepted
			completed := make(chan error, 3)
			prepare := func(finishCurrent bool) {
				go func() {
					payload, failure := system.AskGrain(ctx, target, agentactor.PrepareClose{
						CreationID:    "test",
						FinishCurrent: finishCurrent,
					}, 5*time.Second)
					if failure == nil {
						failure = payload.(agentactor.Prepared).Err
					}
					completed <- failure
				}()
				select {
				case <-accepted:
				case <-time.After(time.Second):
					t.Fatal("close not admitted")
				}
			}
			prepare(true)
			prepare(true)
			select {
			case failure := <-completed:
				t.Fatalf("prepared while model active: %v", failure)
			default:
			}
			for _, input := range []any{
				Followup{
					CreationID: "test",
					Message:    queued,
				},
				Steer{
					CreationID: "test",
					Message:    queued,
				},
				Inject{
					CreationID: "test",
					Message:    queued,
				},
				Replace{
					CreationID: "test",
					MessageID:  queued.StableID(),
					Message:    queued,
				},
				Promote{
					CreationID: "test",
					MessageID:  queued.StableID(),
				},
			} {
				payload, err = system.AskGrain(ctx, target, input, time.Second)
				if err != nil || !errors.Is(payload.(Changed).Err, agent.ErrClosing) {
					t.Fatalf("new work accepted %T: %v %v", input, payload, err)
				}
			}
			select {
			case <-backend.canceled:
				t.Fatal("FinishCurrent cancelled model")
			default:
			}
			replies := 2
			if cancelWork {
				prepare(false)
				replies++
				select {
				case <-backend.canceled:
				case <-time.After(time.Second):
					t.Fatal("ordinary close did not cancel model")
				}
			} else {
				close(backend.release)
			}
			for range replies {
				select {
				case failure := <-completed:
					if failure != nil {
						t.Fatal(failure)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("close preparation never finished")
				}
			}
			payload, err = system.AskGrain(ctx, target, ReadActivity{
				CreationID: "test",
			}, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			observed := payload.(Activity)
			if observed.Err != nil || observed.Status != agent.StatusIdle || len(observed.Pending.NextTurn) != 1 || observed.Pending.NextTurn[0].StableID() != queued.StableID() {
				t.Fatalf("incorrect final activity: %+v", observed)
			}
			payload, err = system.AskGrain(ctx, storage, sessionactor.Read{
				CreationID: "test",
			}, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			counts := map[string]int{}
			for _, entry := range payload.(sessionactor.View).Snapshot.Events {
				counts[entry.Type]++
			}
			if counts[session.TurnStartEventName] != 1 || counts[session.TurnEndEventName] != 1 {
				t.Fatalf("wrong Turn history: %v", counts)
			}
			if !cancelWork && backend.calls.Load() != 1 {
				t.Fatalf("model calls: %d", backend.calls.Load())
			}
			payload, err = system.AskGrain(ctx, target, agentactor.CloseResources{
				CreationID: "test",
			}, time.Second)
			if err != nil || payload.(agentactor.Closed).Err != nil {
				t.Fatalf("resources remained: %v %v", payload, err)
			}
		})
	}
}
