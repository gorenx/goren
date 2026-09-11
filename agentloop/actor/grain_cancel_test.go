package loopactor

import (
	"context"
	"testing"
	"time"

	"github.com/gorenx/goren/agent"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
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
			target, err := goakt.GrainOf[*executionGrain](ctx, system, "execution", goakt.WithLongLivedGrain())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := system.AskGrain(ctx, target, executionSetup{
				execute: true, subject: subject,
				target:     storage,
				prompt:     endpoint,
				middleware: endpoint,
			}, time.Second); err != nil {
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
			payload, err = system.AskGrain(ctx, target, ReadActivity{CreationID: "test"}, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if payload.(Activity).Status != agent.StatusRunning {
				t.Fatal("cancellation acknowledgement finished execution")
			}
			if _, err := system.AskGrain(ctx, endpoint, releaseStopping{}, time.Second); err != nil {
				t.Fatal(err)
			}
			payload, err = system.AskGrain(ctx, target, WhenIdle{CreationID: "test"}, time.Second)
			if err != nil || payload.(Completion).Err != nil {
				t.Fatalf("cancelled execution did not settle: %v, %v", payload, err)
			}
			payload, err = system.AskGrain(ctx, storage, readCounts{}, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if payload.(sessionCounts).commits != 3 {
				t.Fatalf("canceled preparation opened Step: %+v", payload)
			}
		})
	}
}
