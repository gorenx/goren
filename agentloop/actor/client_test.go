package loopactor_test

import (
	"context"
	agentactor "github.com/gorenx/goren/agent/actor"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorenx/goren/agent"
	agentregistry "github.com/gorenx/goren/agent/registry"
	loopactor "github.com/gorenx/goren/agentloop/actor"
	"github.com/gorenx/goren/agentmessage"
	sessionactor "github.com/gorenx/goren/session/actor"
	"github.com/gorenx/goren/session/persistence/sqlite"
	sessionregistry "github.com/gorenx/goren/session/registry"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
	"github.com/tochemey/goakt/v4/reentrancy"
)

type clientDestination struct {
	target     *goakt.GrainIdentity
	creationID string
}

type clientCaller struct {
	sender *loopactor.Client
}

func (*clientCaller) OnActivate(context.Context, *goakt.GrainProps) error   { return nil }
func (*clientCaller) OnDeactivate(context.Context, *goakt.GrainProps) error { return nil }
func (fixture *clientCaller) OnReceive(gc *goakt.GrainContext) {
	reply := gc.DeferResponse()
	switch input := gc.Message().(type) {
	case clientDestination:
		if err := gc.EnableReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))); err != nil {
			panic(err)
		}
		var err error
		fixture.sender, err = loopactor.NewClient(input.target, input.creationID)
		reply.Response(loopactor.Completion{
			Err: err,
		})
	case loopactor.Inject:
		fixture.sender.Inject(gc, input.Message).Then(func(result loopactor.Changed) { reply.Response(result) })
	case loopactor.Followup:
		fixture.sender.Followup(gc, input.Message).Then(func(result loopactor.Changed) { reply.Response(result) })
	case loopactor.Cancel:
		fixture.sender.Cancel(gc, input.Cause, input.Options).Then(func(result loopactor.Changed) { reply.Response(result) })
	case loopactor.WhenIdle:
		fixture.sender.WhenIdle(gc).Then(func(result loopactor.Completion) { reply.Response(result) })
	default:
		gc.Unhandled()
	}
}

func TestClientSeparatesInputCancellationAndIdle(t *testing.T) {
	ctx := context.Background()
	backend, err := sqlite.Open(ctx, sqlite.Config{
		Path:        filepath.Join(t.TempDir(), "sessions.sqlite"),
		JournalMode: sqlite.JournalWAL,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := backend.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	system, err := goakt.NewActorSystem("agent-client", goakt.WithLogger(log.DiscardLogger))
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
	directory, err := goakt.GrainOf[*sessionregistry.RegistryGrain](ctx, system, sessionregistry.RegistryName, goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	driver, replies := grainDriver(t, system, directory)
	if result := request[sessionactor.Initialized](t, driver, replies, sessionregistry.CreateSession{
		SessionID:  "client-test",
		CreationID: "current",
		Storage:    backend,
	}); result.Err != nil {
		t.Fatal(result.Err)
	}
	authority, err := goakt.GrainOf[*agentregistry.Grain](ctx, system, agentregistry.Name, goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	driver, replies = grainDriver(t, system, authority)
	if result := request[agentregistry.Completion](t, driver, replies, agentregistry.ReserveAgent{
		Creation: agentregistry.Creation{
			AgentID:    "client-test",
			CreationID: "current",
		},
	}); result.Err != nil {
		t.Fatal(result.Err)
	}
	identity, err := goakt.GrainOf[*loopactor.Grain](ctx, system, loopactor.RuntimeName("client-test"), goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	driver, replies = grainDriver(t, system, identity)
	dependencies := configurationRoutes(t, system)
	if result := request[agentactor.Initialized](t, driver, replies, agentactor.Initialize{
		AgentID:     "client-test",
		Authority:   authority,
		CreationID:  "current",
		Session:     sessionRoute(t, system, "client-test"),
		Prompt:      promptRoute(t, system, "client-test", dependencies),
	}); result.Err != nil {
		t.Fatal(result.Err)
	}
	caller, err := goakt.GrainOf[*clientCaller](ctx, system, "client-caller", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	driver, replies = grainDriver(t, system, caller)
	if result := request[loopactor.Completion](t, driver, replies, clientDestination{
		target:     identity,
		creationID: "current",
	}); result.Err != nil {
		t.Fatal(result.Err)
	}
	input, err := agentmessage.NewUserMessage(agentmessage.UserMessageInput{
		Content: []agentmessage.ContentBlock{agentmessage.NewTextBlock("injected")},
		Source:  agentmessage.UserMessageSource{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result := request[loopactor.Changed](t, driver, replies, loopactor.Inject{
		Message: input,
	}); result.Err != nil || len(result.Changes.Inserted) != 1 {
		t.Fatal("Inject", result)
	}
	// Pending non-waking input does not prevent an idle acknowledgement.
	if result := request[loopactor.Completion](t, driver, replies, loopactor.WhenIdle{}); result.Err != nil {
		t.Fatal(result.Err)
	}
	input, err = agentmessage.NewUserMessage(agentmessage.UserMessageInput{
		Content: []agentmessage.ContentBlock{agentmessage.NewTextBlock("execute")},
		Source:  agentmessage.UserMessageSource{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result := request[loopactor.Changed](t, driver, replies, loopactor.Followup{
		Message: input,
	}); result.Err != nil {
		t.Fatal(result.Err)
	}
	if err := goakt.Tell(ctx, driver, command{
		payload: loopactor.WhenIdle{},
	}); err != nil {
		t.Fatal(err)
	}
	// The same caller must still deliver Cancel while its idle request is pending.
	if result := request[loopactor.Changed](t, driver, replies, loopactor.Cancel{
		Cause: agent.UserCancel{},
	}); result.Err != nil || len(result.Changes.Discarded) != 2 {
		t.Fatal("Cancel", result)
	}
	// No driver completion has happened: cancellation delivery is not idle.
	select {
	case result := <-replies:
		t.Fatalf("premature idle acknowledgement: %+v", result)
	default:
	}
	if err := system.TellGrain(ctx, identity, new(goakt.PoisonPill)); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-replies:
		completed, ok := result.value.(loopactor.Completion)
		if result.err == nil && (!ok || completed.Err == nil) {
			t.Fatalf("stop falsely confirmed idle: %+v", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("idle request leaked after deactivation")
	}
}
