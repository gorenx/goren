package loopactor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gorenx/goren/agent"
	agentactor "github.com/gorenx/goren/agent/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
)

type maintenanceReceiver struct{}

func (*maintenanceReceiver) OnActivate(context.Context, *goakt.GrainProps) error   { return nil }
func (*maintenanceReceiver) OnDeactivate(context.Context, *goakt.GrainProps) error { return nil }
func (*maintenanceReceiver) OnReceive(gc *goakt.GrainContext) {
	input, ok := gc.Message().(agentactor.CancelMaintenance)
	if !ok {
		gc.Unhandled()
		return
	}
	var failure error
	var cancelled agent.Cancellation
	if input.RequestID != "maintenance" || !errors.As(input.Cause, &cancelled) {
		failure = errors.New("incorrect cancellation identity or cause")
	}
	gc.Response(agentactor.MaintenanceCancelled{
		Err: failure,
	})
}

func TestMaintenanceProtocolKeepsOwnershipUntilMatchingFinish(t *testing.T) {
	ctx := context.Background()
	system, err := goakt.NewActorSystem("maintenance-owner", goakt.WithLogger(log.DiscardLogger))
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
	subject, _ := acceptedTurn(t)
	if _, err := subject.FinishActivity(); err != nil {
		t.Fatal(err)
	}
	target, err := goakt.GrainOf[*settlementAgent](ctx, system, "agent", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	worker, err := goakt.GrainOf[*maintenanceReceiver](ctx, system, "worker", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	ask := func(message any) any {
		t.Helper()
		reply, err := system.AskGrain(ctx, target, message, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		return reply
	}
	ask(settlementSetup{
		subject: subject,
	})
	begin := agentactor.BeginMaintenance{
		CreationID: "test",
		RequestID:  "maintenance",
		Worker:     worker,
	}
	if reply := ask(begin).(agentactor.MaintenanceAccepted); reply.Err != nil {
		t.Fatal(reply.Err)
	}
	if reply := ask(begin).(agentactor.MaintenanceAccepted); reply.Err == nil {
		t.Fatal("duplicate maintenance accepted")
	}
	if reply := ask(startTurn{}).(turnStarted); reply.err == nil {
		t.Fatal("Turn started during maintenance")
	}
	if reply := ask(Cancel{
		CreationID: "test",
		Cause:      agent.UserCancel{},
		Options: agent.CancelOptions{
			KeepInbox: true,
		},
	}).(Changed); reply.Err != nil {
		t.Fatal(reply.Err)
	}
	if reply := ask(agentactor.FinishMaintenance{
		CreationID: "test",
		RequestID:  "old",
	}).(agentactor.MaintenanceFinished); reply.Err == nil {
		t.Fatal("stale completion released maintenance")
	}
	if reply := ask(begin).(agentactor.MaintenanceAccepted); reply.Err == nil {
		t.Fatal("cancel released maintenance")
	}
	if reply := ask(agentactor.FinishMaintenance{
		CreationID: "test",
		RequestID:  "maintenance",
	}).(agentactor.MaintenanceFinished); reply.Err != nil {
		t.Fatal(reply.Err)
	}
	if reply := ask(WhenIdle{
		CreationID: "test",
	}).(Completion); reply.Err != nil {
		t.Fatal(reply.Err)
	}
	begin.RequestID = "next"
	if reply := ask(begin).(agentactor.MaintenanceAccepted); reply.Err != nil {
		t.Fatal(reply.Err)
	}
	if reply := ask(agentactor.FinishMaintenance{
		CreationID: "test",
		RequestID:  "maintenance",
	}).(agentactor.MaintenanceFinished); reply.Err == nil {
		t.Fatal("old completion released new maintenance")
	}
	if reply := ask(agentactor.FinishMaintenance{
		CreationID: "test",
		RequestID:  "next",
	}).(agentactor.MaintenanceFinished); reply.Err != nil {
		t.Fatal(reply.Err)
	}
}
