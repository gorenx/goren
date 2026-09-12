package spike

import (
	"context"
	"fmt"
	"testing"
	"time"

	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
)

// routerWorker isolates framework startup from model and Companion behavior.
type routerWorker struct{}

func (*routerWorker) PreStart(*goakt.Context) error { return nil }
func (*routerWorker) PostStop(*goakt.Context) error { return nil }
func (*routerWorker) Receive(incoming *goakt.ReceiveContext) {
	if _, started := incoming.Message().(*goakt.PostStart); !started {
		incoming.Response("ready")
	}
}

func TestRouterStartupRegistersEveryRoutee(t *testing.T) {
	system, err := goakt.NewActorSystem("router-startup", goakt.WithLogger(log.DiscardLogger))
	if err != nil {
		t.Fatal(err)
	}
	if err := system.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := system.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})
	for index := range 1000 {
		pool, err := system.SpawnRouter(t.Context(), fmt.Sprintf("pool-%d", index), 1, &routerWorker{})
		if err != nil {
			t.Fatal(err)
		}
		payload, err := goakt.Ask(t.Context(), pool, &goakt.GetRoutees{}, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		members, ok := payload.(*goakt.Routees)
		if !ok || len(members.Names()) != 1 {
			t.Fatalf("routees: %v", payload)
		}
		name := members.Names()[0]
		_, lookupErr := system.ActorOf(t.Context(), name)
		_, childErr := pool.Child(name)
		if lookupErr != nil || childErr != nil {
			t.Fatalf("router %d returned routee %q absent from tree: lookup=%v child=%v", index, name, lookupErr, childErr)
		}
		if err := pool.Shutdown(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
}
