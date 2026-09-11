package companionactor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gorenx/goren/agent"
	agentregistry "github.com/gorenx/goren/agent/registry"
	sessionactor "github.com/gorenx/goren/session/actor"
	sessionregistry "github.com/gorenx/goren/session/registry"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

type delayedRead struct {
	entered chan struct{}
	result  sessionactor.View
}

type releaseRead struct{}

// readBarrier holds a completed read reply so shutdown can race with startup
// without sleeps or a blocked actor mailbox.
type readBarrier struct {
	input delayedRead
	reply *goakt.GrainReply
}

func (*readBarrier) OnActivate(context.Context, *goakt.GrainProps) error   { return nil }
func (*readBarrier) OnDeactivate(context.Context, *goakt.GrainProps) error { return nil }
func (owner *readBarrier) OnReceive(gc *goakt.GrainContext) {
	switch input := gc.Message().(type) {
	case delayedRead:
		owner.input = input
		gc.Response(struct{}{})
	case sessionactor.Read:
		owner.reply = gc.DeferResponse()
		close(owner.input.entered)
	case releaseRead:
		owner.reply.Response(owner.input.result)
		owner.reply = nil
		gc.Response(struct{}{})
	default:
		gc.Unhandled()
	}
}

func TestStewardCloseDuringRecoveryDoesNotStartChildren(t *testing.T) {
	fixture := newStewardship(t)
	if _, err := fixture.client.Create(t.Context(), Create{
		Name:    "recovered",
		Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	previous := awaitPhase(t, fixture.client, "recovered", Ready)
	if err := fixture.client.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	conversation := request[sessionregistry.Endpoint](t, fixture.system, fixture.sessions, sessionregistry.ResolveSession{
		SessionID:  fixture.input.Creation.AgentID,
		CreationID: fixture.input.Creation.CreationID,
	})
	if conversation.Err != nil {
		t.Fatal(conversation.Err)
	}
	current := request[sessionactor.View](t, fixture.system, conversation.Target, sessionactor.Read{
		CreationID: fixture.input.Creation.CreationID,
	})
	if current.Err != nil {
		t.Fatal(current.Err)
	}
	barrier, err := goakt.GrainOf[*readBarrier](t.Context(), fixture.system, "read-barrier",
		goakt.WithLongLivedGrain(),
		goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))),
	)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	request[struct{}](t, fixture.system, barrier, delayedRead{
		entered: entered,
		result:  current,
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	opened := make(chan error, 1)
	go func() {
		_, failure := Open(ctx, fixture.system, Initialization{
			Parent:   fixture.input.Creation,
			Registry: fixture.authority,
			Session:  barrier,
			Creator:  fixture.creator,
			Settings: fixture.creator.settings,
		})
		opened <- failure
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("recovery did not reach Session read")
	}
	stale := *fixture.client
	stale.parent.CreationID = "stale"
	if err := stale.Close(ctx); !errors.Is(err, agentregistry.ErrCreation) {
		t.Fatalf("stale parent canceled recovery: %v", err)
	}
	if err := fixture.client.Close(ctx); err != nil {
		t.Fatalf("initializing Steward could not close: %v", err)
	}
	select {
	case failure := <-opened:
		if !errors.Is(failure, agent.ErrClosing) {
			t.Fatalf("interrupted initialization = %v", failure)
		}
	case <-ctx.Done():
		t.Fatal("close did not settle initialization")
	}
	items, err := fixture.client.List(ctx)
	if err != nil || len(items) != 0 {
		t.Fatalf("interrupted recovery adopted relationships: %+v, %v", items, err)
	}
	missing := request[agentregistry.Ready](t, fixture.system, fixture.authority, agentregistry.FindAgent{
		AgentID: previous.Companion.ChildSessionID,
	})
	if missing.Err == nil {
		t.Fatal("interrupted recovery started child")
	}
	if err := fixture.client.Close(ctx); err != nil {
		t.Fatal("repeated close failed", err)
	}
	// Bind again before allowing the old transport reply to arrive. The reply
	// belongs to the canceled initialization, never the new one.
	endpoint, err := Open(ctx, fixture.system, Initialization{
		Parent:   fixture.input.Creation,
		Registry: fixture.authority,
		Session:  conversation.Target,
		Creator:  fixture.creator,
		Settings: fixture.creator.settings,
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.client = endpoint
	request[struct{}](t, fixture.system, barrier, releaseRead{})
	restored := awaitPhase(t, fixture.client, "recovered", Ready)
	if restored.Companion.ChildSessionID != previous.Companion.ChildSessionID || restored.Creation == previous.Creation {
		t.Fatal("recovery did not retain child Session with a fresh lifetime")
	}
}

func TestStewardCloseAfterInitializationFailure(t *testing.T) {
	fixture := newStewardship(t)
	if err := fixture.client.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	conversation := request[sessionregistry.Endpoint](t, fixture.system, fixture.sessions, sessionregistry.ResolveSession{
		SessionID:  fixture.input.Creation.AgentID,
		CreationID: fixture.input.Creation.CreationID,
	})
	invalid := fixture.input.Creation
	invalid.CreationID = "unregistered"
	_, err := Open(t.Context(), fixture.system, Initialization{
		Parent:   invalid,
		Registry: fixture.authority,
		Session:  conversation.Target,
		Creator:  fixture.creator,
		Settings: fixture.creator.settings,
	})
	if !errors.Is(err, agentregistry.ErrCreation) {
		t.Fatal("invalid parent initialized Steward", err)
	}
	failed := *fixture.client
	failed.parent = invalid
	if err := failed.Close(t.Context()); err != nil {
		t.Fatal("failed initialization could not settle close", err)
	}
	// Cleanup closes the last binding, even though its initialization failed.
	fixture.client = &failed
}
