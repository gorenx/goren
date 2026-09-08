package loopactor

import (
	"context"
	"testing"
	"time"

	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
	"github.com/tochemey/goakt/v4/reentrancy"
)

type preparationRequest struct {
	target     *goakt.GrainIdentity
	middleware *goakt.GrainIdentity
}

type preparationCaller struct {
	replies chan stepPrepared
}

func (*preparationCaller) PreStart(*goakt.Context) error { return nil }
func (*preparationCaller) PostStop(*goakt.Context) error { return nil }
func (owner *preparationCaller) Receive(rc *goakt.ReceiveContext) {
	if input, ok := rc.Message().(preparationRequest); ok {
		replies := owner.replies
		rc.RequestGrain(input.target, prepareStep{
			middleware: input.middleware,
		}).Then(func(payload any, failure error) {
			result, ok := payload.(stepPrepared)
			if failure != nil {
				result.err = failure
			} else if !ok {
				panic("unexpected Step reply")
			}
			replies <- result
		})
	}
}

func TestActorPreparesStepAndReconcilesLostStartReply(t *testing.T) {
	for _, failure := range []string{"", "step reply", "claim reply", "claim rejected"} {
		name := failure
		if name == "" {
			name = "normal"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			system, err := goakt.NewActorSystem("step", goakt.WithLogger(log.DiscardLogger))
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
				failure: failure,
			}, time.Second); err != nil {
				t.Fatal(err)
			}
			identity, err := goakt.GrainOf[*settlementAgent](ctx, system, "execution", goakt.WithLongLivedGrain())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := system.AskGrain(ctx, identity, settlementSetup{
				subject: subject,
				target:  storage,
			}, time.Second); err != nil {
				t.Fatal(err)
			}
			replies := make(chan stepPrepared, 1)
			driver, err := system.Spawn(ctx, "caller", &preparationCaller{
				replies: replies,
			}, goakt.WithReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
			if err != nil {
				t.Fatal(err)
			}
			if err := goakt.Tell(ctx, driver, preparationRequest{
				target:     identity,
				middleware: storage,
			}); err != nil {
				t.Fatal(err)
			}
			select {
			case result := <-replies:
				if (result.err != nil) != (failure != "") || result.entered == (failure != "") {
					t.Fatalf("preparation = %+v", result)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("Step preparation timed out")
			}
			payload, err := system.AskGrain(ctx, storage, readCounts{}, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			expected := storageCounts{
				commits:    3,
				reads:      1,
				prompts:    1,
				admissions: 1,
			}
			if failure == "step reply" {
				expected.commits = 2
				expected.reads = 2
			}
			if failure == "claim reply" || failure == "claim rejected" {
				expected = storageCounts{
					commits: 1,
					reads:   1,
				}
			}
			if actual := payload.(storageCounts); actual != expected {
				t.Fatalf("operations = %+v; want %+v", actual, expected)
			}
		})
	}
}
