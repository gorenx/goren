package loopactor

import (
	"context"
	"testing"
	"time"

	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
)

func TestTurnStartCommitsOrReconcilesWithoutDuplicateStart(t *testing.T) {
	for _, scenario := range []string{"normal", "turn reply", "turn rejected", "pending candidate"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			system, err := goakt.NewActorSystem("turn-start", goakt.WithLogger(log.DiscardLogger))
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
			if scenario == "pending candidate" {
				if _, err := subject.PrepareTurn(); err != nil {
					t.Fatal(err)
				}
			}
			storage, err := goakt.GrainOf[*settlementStorage](ctx, system, "storage", goakt.WithLongLivedGrain())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := system.AskGrain(ctx, storage, storageSetup{
				history: history,
				failure: scenario,
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
			}, time.Second); err != nil {
				t.Fatal(err)
			}
			payload, err := system.AskGrain(ctx, target, startTurn{}, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			result := payload.(turnStarted)
			wantOpen := scenario == "normal" || scenario == "pending candidate"
			wantError := scenario == "turn reply" || scenario == "turn rejected"
			if result.opened != wantOpen || (result.err != nil) != wantError {
				t.Fatalf("start: %+v", result)
			}
			if !wantOpen {
				payload, err = system.AskGrain(ctx, target, startTurn{}, time.Second)
				if err != nil || payload.(turnStarted).err != nil || !payload.(turnStarted).opened {
					t.Fatalf("retry: %v, %v", payload, err)
				}
			}
			payload, err = system.AskGrain(ctx, storage, readCounts{}, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			counts := payload.(storageCounts)
			wantCommits := 1
			if scenario == "turn rejected" || scenario == "turn reply" {
				wantCommits = 2
			}
			wantReads := 0
			if counts.commits != wantCommits || counts.reads != wantReads {
				t.Fatalf("unexpected requests: %+v", counts)
			}
			// Repeating a successful start must fail without another Session write.
			payload, err = system.AskGrain(ctx, target, startTurn{}, time.Second)
			if err != nil || payload.(turnStarted).err == nil {
				t.Fatalf("duplicate start accepted: %v, %v", payload, err)
			}
		})
	}
}
