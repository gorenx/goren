package loopactor

import (
	"context"
	"errors"
	"github.com/gorenx/goren/agentloop"
	"github.com/gorenx/goren/tools"
	runtimeactor "github.com/gorenx/goren/tools/runtime/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
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
				execute:    true,
				subject:    subject,
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
			payload, err = system.AskGrain(ctx, target, ReadActivity{
				CreationID: "test",
			}, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if payload.(Activity).Status != agent.StatusRunning {
				t.Fatal("cancellation acknowledgement finished execution")
			}
			if _, err := system.AskGrain(ctx, endpoint, releaseStopping{}, time.Second); err != nil {
				t.Fatal(err)
			}
			payload, err = system.AskGrain(ctx, target, WhenIdle{
				CreationID: "test",
			}, time.Second)
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

// heldToolRelease models a Runtime which has released the call but whose reply
// has not yet reached the Agent mailbox.
type heldToolRelease struct {
	reply *goakt.GrainReply
}

type confirmToolRelease struct{}
type releaseObserved struct{}

func (*heldToolRelease) OnActivate(context.Context, *goakt.GrainProps) error   { return nil }
func (*heldToolRelease) OnDeactivate(context.Context, *goakt.GrainProps) error { return nil }
func (endpoint *heldToolRelease) OnReceive(gc *goakt.GrainContext) {
	switch gc.Message().(type) {
	case runtimeactor.ReleaseCall:
		endpoint.reply = gc.DeferResponse()
	case runtimeactor.CancelCall:
		gc.Response(runtimeactor.Completion{
			Err: errors.New("tools: call is not registered"),
		})
	case releaseObserved:
		gc.Response(endpoint.reply != nil)
	case confirmToolRelease:
		endpoint.reply.Response(runtimeactor.Completion{})
		endpoint.reply = nil
		gc.Response(struct{}{})
	}
}

type toolReleaseSetup struct {
	target *goakt.GrainIdentity
	result chan error
}
type cancelToolRelease struct{}
type readReleaseActivity struct{}

type toolReleaseProbe struct{ Grain }

func (owner *toolReleaseProbe) OnReceive(gc *goakt.GrainContext) {
	switch input := gc.Message().(type) {
	case toolReleaseSetup:
		owner.creationID = "test"
		owner.tools = input.target
		owner.calls = map[string]toolCallPhase{"operation": toolCallActive}
		failure := (toolPort{
			scope: serviceScope{
				owner: &owner.Grain,
				gc:    gc,
			},
		}).Release("operation", &toolReleaseReceiver{
			result: input.result,
		})
		gc.Response(Completion{
			Err: failure,
		})
	case cancelToolRelease:
		owner.cancelOperations(gc, completeCancel{
			reply: gc.DeferResponse(),
		})
	case readReleaseActivity:
		gc.Response(owner.busy())
	default:
		owner.Grain.OnReceive(gc)
	}
}

type toolReleaseReceiver struct{ result chan error }

func (*toolReleaseReceiver) ToolPrepared(agentloop.Services, tools.ScheduledToolStage, error) {
	panic("unexpected prepare")
}
func (*toolReleaseReceiver) ToolDispatched(agentloop.Services, error) { panic("unexpected dispatch") }
func (*toolReleaseReceiver) ToolFinalized(agentloop.Services, tools.ToolExecutionResult, error) {
	panic("unexpected finalize")
}
func (receiver *toolReleaseReceiver) ToolReleased(_ agentloop.Services, failure error) {
	receiver.result <- failure
}

func TestCancellationDoesNotTargetCallAwaitingReleaseAcknowledgement(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	system, err := goakt.NewActorSystem("cancel-released-tool", goakt.WithLogger(log.DiscardLogger))
	if err != nil {
		t.Fatal(err)
	}
	if err := system.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer system.Stop(context.Background())
	endpoint, err := goakt.GrainOf[*heldToolRelease](ctx, system, "tools", goakt.WithLongLivedGrain(), goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		t.Fatal(err)
	}
	target, err := goakt.GrainOf[*toolReleaseProbe](ctx, system, "agent", goakt.WithLongLivedGrain(), goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		t.Fatal(err)
	}
	settled := make(chan error, 1)
	payload, err := system.AskGrain(ctx, target, toolReleaseSetup{
		target: endpoint,
		result: settled,
	}, time.Second)
	if err != nil || payload.(Completion).Err != nil {
		t.Fatalf("release admission: %v %v", payload, err)
	}
	payload, err = system.AskGrain(ctx, endpoint, releaseObserved{}, time.Second)
	if err != nil || !payload.(bool) {
		t.Fatalf("release was not sent: %v %v", payload, err)
	}
	payload, err = system.AskGrain(ctx, target, cancelToolRelease{}, time.Second)
	if err != nil || payload.(Changed).Err != nil {
		t.Fatalf("cancel after release: %v %v", payload, err)
	}
	payload, err = system.AskGrain(ctx, target, readReleaseActivity{}, time.Second)
	if err != nil || !payload.(bool) {
		t.Fatal("pending release acknowledgement was treated as idle")
	}
	if _, err := system.AskGrain(ctx, endpoint, confirmToolRelease{}, time.Second); err != nil {
		t.Fatal(err)
	}
	select {
	case failure := <-settled:
		if failure != nil {
			t.Fatal(failure)
		}
	case <-ctx.Done():
		t.Fatal("release acknowledgement did not settle")
	}
	payload, err = system.AskGrain(ctx, target, readReleaseActivity{}, time.Second)
	if err != nil || payload.(bool) {
		t.Fatal("confirmed release retained activity")
	}
}
