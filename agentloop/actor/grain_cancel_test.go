package loopactor

import (
	"context"
	"testing"
	"time"

	"github.com/gorenx/goren/agent"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
	"github.com/tochemey/goakt/v4/reentrancy"
)

func TestPreparationCancellationAwaitsOnlyActiveComponent(t *testing.T) {
	for _, phase := range []string{"prompt", "pre-step"} {
		t.Run(phase, func(t *testing.T) {
			ctx := context.Background()
			system, err := goakt.NewActorSystem("cancel-preparation", goakt.WithLogger(log.DiscardLogger))
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
			storage, err := goakt.GrainOf[*settlementStorage](ctx, system, "storage", goakt.WithLongLivedGrain())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := system.AskGrain(ctx, storage, storageSetup{
				history: history,
			}, time.Second); err != nil {
				t.Fatal(err)
			}
			endpoint, err := goakt.GrainOf[*stoppingEndpoint](ctx, system, "component", goakt.WithLongLivedGrain())
			if err != nil {
				t.Fatal(err)
			}
			entered := make(chan struct{})
			if _, err := system.AskGrain(ctx, endpoint, stoppingSetup{
				phase:   phase,
				entered: entered,
			}, time.Second); err != nil {
				t.Fatal(err)
			}
			target, err := goakt.GrainOf[*settlementAgent](ctx, system, "execution", goakt.WithLongLivedGrain())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := system.AskGrain(ctx, target, settlementSetup{
				subject: subject,
				target:  storage,
				prompt:  endpoint,
			}, time.Second); err != nil {
				t.Fatal(err)
			}
			replies := make(chan stepPrepared, 1)
			caller, err := system.Spawn(ctx, "caller", &preparationCaller{
				replies: replies,
			}, goakt.WithReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
			if err != nil {
				t.Fatal(err)
			}
			if err := goakt.Tell(ctx, caller, preparationRequest{
				target:     target,
				middleware: endpoint,
			}); err != nil {
				t.Fatal(err)
			}
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("component did not start")
			}
			payload, err := system.AskGrain(ctx, target, Cancel{
				CreationID: "test",
				Cause:      agent.UserCancel{},
				Options: agent.CancelOptions{
					KeepInbox: true,
				},
			}, time.Second)
			if err != nil || payload.(Changed).Err != nil {
				t.Fatalf("cancellation: %v, %v", payload, err)
			}
			select {
			case result := <-replies:
				t.Fatalf("cancellation acknowledgement finished preparation: %+v", result)
			default:
			}
			if _, err := system.AskGrain(ctx, endpoint, releaseStopping{}, time.Second); err != nil {
				t.Fatal(err)
			}
			select {
			case result := <-replies:
				if result.err == nil || result.entered {
					t.Fatalf("canceled preparation: %+v", result)
				}
			case <-time.After(time.Second):
				t.Fatal("preparation did not finish")
			}
			payload, err = system.AskGrain(ctx, storage, readCounts{}, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if payload.(storageCounts).commits != 1 {
				t.Fatalf("canceled preparation opened Step: %+v", payload)
			}
		})
	}
}
