package spike

import (
	"context"
	"testing"
	"time"

	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
)

// childCreation carries a trusted PID in this framework-only experiment.
// It does not model Agent creation authorization or a public tool argument.
type childCreation struct {
	Parent *goakt.PID
	Name   string
}

type childCreated struct {
	Child *goakt.PID
	Err   error
}

// childCreator executes the spawn in a different Actor from the physical parent.
type childCreator struct{}

func (*childCreator) PreStart(*goakt.Context) error { return nil }
func (*childCreator) PostStop(*goakt.Context) error { return nil }
func (*childCreator) Receive(rc *goakt.ReceiveContext) {
	if input, ok := rc.Message().(childCreation); ok {
		endpoint, err := input.Parent.SpawnChild(rc.Context(), input.Name, &topologyProbe{}, goakt.WithLongLived())
		rc.Response(childCreated{
			Child: endpoint,
			Err:   err,
		})
	}
}

// ChildCreatorGrain proves the initiating owner need not itself be a PID Actor.
// The current ToolRuntime uses the same Grain execution model, but is not wired here.
type ChildCreatorGrain struct{}

func (*ChildCreatorGrain) OnActivate(context.Context, *goakt.GrainProps) error   { return nil }
func (*ChildCreatorGrain) OnDeactivate(context.Context, *goakt.GrainProps) error { return nil }
func (*ChildCreatorGrain) OnReceive(gc *goakt.GrainContext) {
	if input, ok := gc.Message().(childCreation); ok {
		endpoint, err := input.Parent.SpawnChild(gc.Context(), input.Name, &topologyProbe{}, goakt.WithLongLived())
		gc.Response(childCreated{
			Child: endpoint,
			Err:   err,
		})
	}
}

func TestActorCreatesChildForAnotherActor(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	system, err := goakt.NewActorSystem("externalcreator", goakt.WithLogger(log.DiscardLogger))
	if err != nil {
		t.Fatal(err)
	}
	if err = system.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = system.Stop(context.Background()) })
	parent, err := system.Spawn(ctx, "agent", &topologyProbe{}, goakt.WithLongLived())
	if err != nil {
		t.Fatal(err)
	}
	creator, err := system.Spawn(ctx, "tool-runtime", &childCreator{}, goakt.WithLongLived())
	if err != nil {
		t.Fatal(err)
	}
	payload, err := goakt.Ask(ctx, creator, childCreation{
		Parent: parent,
		Name:   "companion",
	}, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	result, valid := payload.(childCreated)
	if !valid || result.Err != nil || result.Child == nil {
		t.Fatalf("spawn result: %#v", payload)
	}
	if !result.Child.Parent().Equals(parent) {
		t.Fatal("child belongs to the wrong parent")
	}
	if len(parent.Children()) != 1 || len(creator.Children()) != 0 {
		t.Fatal("creator acquired the target parent's child")
	}
	if err = creator.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if !result.Child.IsRunning() {
		t.Fatal("stopping the creator stopped another parent's child")
	}
	if err = parent.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if result.Child.IsRunning() {
		t.Fatal("stopping the actual parent left its child running")
	}
}

func TestGrainCreatesChildForAnotherActor(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	system, err := goakt.NewActorSystem("graincreator", goakt.WithLogger(log.DiscardLogger))
	if err != nil {
		t.Fatal(err)
	}
	if err = system.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = system.Stop(context.Background()) })
	parent, err := system.Spawn(ctx, "agent", &topologyProbe{}, goakt.WithLongLived())
	if err != nil {
		t.Fatal(err)
	}
	creator, err := goakt.GrainOf[*ChildCreatorGrain](ctx, system, "tool-runtime", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	payload, err := system.AskGrain(ctx, creator, childCreation{
		Parent: parent,
		Name:   "companion",
	}, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	result, valid := payload.(childCreated)
	if !valid || result.Err != nil || result.Child == nil {
		t.Fatalf("spawn result: %#v", payload)
	}
	if !result.Child.Parent().Equals(parent) {
		t.Fatal("child belongs to the wrong parent")
	}
	if err = parent.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if result.Child.IsRunning() {
		t.Fatal("stopping the actual parent left its child running")
	}
	payload, err = system.AskGrain(ctx, creator, childCreation{
		Parent: parent,
		Name:   "after-close",
	}, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	rejected, valid := payload.(childCreated)
	if !valid || rejected.Err == nil || rejected.Child != nil {
		t.Fatalf("closed parent accepted child: %#v", payload)
	}
}
