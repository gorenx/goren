package sessionadapter_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gorenx/goren/agent"
	loopactor "github.com/gorenx/goren/agentloop/actor"
	sessionadapter "github.com/gorenx/goren/apiproxy/session/actor"
	"github.com/gorenx/goren/session"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
)

// endpointFixture isolates typed client mapping from domain behavior tests.
type endpointFixture struct{}

func (*endpointFixture) OnActivate(context.Context, *goakt.GrainProps) error   { return nil }
func (*endpointFixture) OnDeactivate(context.Context, *goakt.GrainProps) error { return nil }
func (*endpointFixture) OnReceive(gc *goakt.GrainContext) {
	switch input := gc.Message().(type) {
	case loopactor.ReadActivity:
		if input.CreationID != "current" {
			gc.Response(loopactor.Activity{
				Err: errors.New("stale creation"),
			})
			return
		}
		gc.Response(loopactor.Activity{
			Status: agent.StatusIdle,
		})
	case loopactor.Remove:
		gc.Response(loopactor.Changed{})
	case loopactor.Replace:
		gc.Response(loopactor.Changed{})
	default:
		gc.Unhandled()
	}
}

func TestAgentClientUsesCreationAndInputResults(t *testing.T) {
	system, err := goakt.NewActorSystem("session-client", goakt.WithLogger(log.DiscardLogger))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := system.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := system.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	endpoint, err := goakt.GrainOf[*endpointFixture](ctx, system, "endpoint", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	parent := session.SessionID("parent")
	subject, err := sessionadapter.NewAgent(system, endpoint, agent.Registration{
		ID:       "conversation",
		ParentID: &parent,
	}, "current", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	parent = "different"
	if *subject.ParentID() != "parent" {
		t.Fatal("registration aliases caller")
	}
	if _, err := subject.Pending(ctx); err != nil {
		t.Fatal(err)
	}
	if err := subject.Remove(ctx, "missing"); !errors.Is(err, agent.ErrInputMissing) {
		t.Fatalf("remove: %v", err)
	}
	stale, err := sessionadapter.NewAgent(system, endpoint, agent.Registration{
		ID: "conversation",
	}, "old", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stale.Pending(ctx); err == nil {
		t.Fatal("stale read accepted")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := subject.Pending(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled read: %v", err)
	}
}
