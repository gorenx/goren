package loopactor_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorenx/goren/agent"
	agentactor "github.com/gorenx/goren/agent/actor"
	agentregistry "github.com/gorenx/goren/agent/registry"
	"github.com/gorenx/goren/agentloop"
	loopactor "github.com/gorenx/goren/agentloop/actor"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	"github.com/gorenx/goren/session/persistence/sqlite"
	sessionregistry "github.com/gorenx/goren/session/registry"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
	"github.com/tochemey/goakt/v4/reentrancy"
)

type command struct {
	payload any
}

type response struct {
	value any
	err   error
}

type grainRequester struct {
	target  *goakt.GrainIdentity
	replies chan response
}

func (*grainRequester) PreStart(*goakt.Context) error { return nil }
func (*grainRequester) PostStop(*goakt.Context) error { return nil }
func (driver *grainRequester) Receive(rc *goakt.ReceiveContext) {
	if input, ok := rc.Message().(command); ok {
		replies := driver.replies
		rc.RequestGrain(driver.target, input.payload).Then(func(value any, err error) {
			replies <- response{
				value: value,
				err:   err,
			}
		})
	}
}
func grainDriver(t *testing.T, system goakt.ActorSystem, identity *goakt.GrainIdentity) (*goakt.PID, <-chan response) {
	t.Helper()
	replies := make(chan response, 4)
	driver, err := system.Spawn(context.Background(), "requester-"+uuid.NewString(), &grainRequester{
		target:  identity,
		replies: replies,
	}, goakt.WithReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		t.Fatal(err)
	}
	return driver, replies
}

func request[T any](t *testing.T, driver *goakt.PID, replies <-chan response, payload any) T {
	t.Helper()
	if err := goakt.Tell(context.Background(), driver, command{
		payload: payload,
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-replies:
		if result.err != nil {
			t.Fatal(result.err)
		}
		value, ok := result.value.(T)
		if !ok {
			t.Fatalf("unexpected reply %T", result.value)
		}
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("request timed out")
		var empty T
		return empty
	}
}

func TestInboxCommandsCommitBeforeApplyingAndRetainStateOnFailure(t *testing.T) {
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
	system, err := goakt.NewActorSystem("agent-inbox", goakt.WithLogger(log.DiscardLogger))
	if err != nil {
		t.Fatal(err)
	}
	if err := system.Start(ctx); err != nil {
		t.Fatal(err)
	}
	dependencies := configurationRoutes(t, system)
	t.Cleanup(func() {
		if err := system.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	directory, err := goakt.GrainOf[*sessionregistry.RegistryGrain](ctx, system, sessionregistry.RegistryName, goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	registryDriver, registryReplies := grainDriver(t, system, directory)
	created := request[sessionactor.Initialized](t, registryDriver, registryReplies, sessionregistry.CreateSession{
		SessionID:  "conversation",
		CreationID: "current",
		Storage:    backend,
	})
	if created.Err != nil {
		t.Fatal(created.Err)
	}
	identity, err := goakt.GrainOf[*loopactor.Grain](ctx, system, loopactor.RuntimeName("conversation"), goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	driver, replies := grainDriver(t, system, identity)
	authority, err := goakt.GrainOf[*agentregistry.Grain](ctx, system, agentregistry.Name, goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	authorityDriver, authorityReplies := grainDriver(t, system, authority)
	denied := request[agentactor.Initialized](t, driver, replies, agentactor.Initialize{
		AgentID:    "conversation",
		Authority:  authority,
		CreationID: "current",
		Session:    sessionRoute(t, system, "conversation"),
		Prompt:     promptRoute(t, system, "conversation", dependencies),
	})
	if denied.Err == nil {
		t.Fatal("unreserved Agent initialized")
	}
	reserved := request[agentregistry.Completion](t, authorityDriver, authorityReplies, agentregistry.Reserve{
		Creation: agentregistry.Creation{
			AgentID:    "conversation",
			CreationID: "current",
		},
	})
	if reserved.Err != nil {
		t.Fatal(reserved.Err)
	}
	initialized := request[agentactor.Initialized](t, driver, replies, agentactor.Initialize{
		AgentID:    "conversation",
		Authority:  authority,
		CreationID: "current",
		Session:    sessionRoute(t, system, "conversation"),
		Prompt:     promptRoute(t, system, "conversation", dependencies),
	})
	if initialized.Err != nil {
		t.Fatal(initialized.Err)
	}
	prompt, err := agentmessage.NewUserMessage(agentmessage.UserMessageInput{
		Content: []agentmessage.ContentBlock{agentmessage.NewTextBlock("hello")},
		Source:  agentmessage.UserMessageSource{},
	})
	if err != nil {
		t.Fatal(err)
	}
	changed := request[loopactor.Changed](t, driver, replies, loopactor.Followup{
		CreationID: "current",
		Message:    prompt,
	})
	if changed.Err != nil || len(changed.Changes.Inserted) != 1 {
		t.Fatalf("append = %#v", changed)
	}
	stale := request[loopactor.Activity](t, driver, replies, loopactor.ReadActivity{
		CreationID: "old",
	})
	if stale.Err == nil || len(stale.Pending.NextTurn) != 0 {
		t.Fatal("stale read exposed current Inbox")
	}
	pending := request[loopactor.Activity](t, driver, replies, loopactor.ReadActivity{
		CreationID: "current",
	})
	if pending.Err != nil || len(pending.Pending.NextTurn) != 1 || len(pending.Pending.NextStep) != 0 {
		t.Fatalf("inbox = %#v", pending)
	}
	closed := request[sessionactor.Closed](t, registryDriver, registryReplies, sessionregistry.CloseSession{
		SessionID:  session.SessionID("conversation"),
		CreationID: "current",
	})
	if closed.Err != nil {
		t.Fatal(closed.Err)
	}
	changed = request[loopactor.Changed](t, driver, replies, loopactor.Remove{
		CreationID: "current",
		MessageID:  prompt.StableID(),
	})
	if changed.Err == nil {
		t.Fatal("closed Session accepted mutation")
	}
	pending = request[loopactor.Activity](t, driver, replies, loopactor.ReadActivity{
		CreationID: "current",
	})
	if pending.Err != nil || len(pending.Pending.NextTurn) != 1 {
		t.Fatal("failed commit changed Inbox")
	}
	if err := system.TellGrain(ctx, identity, new(goakt.PoisonPill)); err != nil {
		t.Fatal(err)
	}
	reactivated := request[agentactor.Initialized](t, driver, replies, agentactor.Initialize{
		AgentID:    "conversation",
		Authority:  authority,
		CreationID: "current",
		Session:    sessionRoute(t, system, "conversation"),
		Prompt:     promptRoute(t, system, "conversation", dependencies),
	})
	if !errors.Is(reactivated.Err, agentregistry.ErrInstance) {
		t.Fatalf("reactivated initialization = %v", reactivated.Err)
	}

}

func TestCloseRetainsSessionUntilPendingInputIsCancelled(t *testing.T) {
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
	system, err := goakt.NewActorSystem("agent-inbox", goakt.WithLogger(log.DiscardLogger))
	if err != nil {
		t.Fatal(err)
	}
	if err := system.Start(ctx); err != nil {
		t.Fatal(err)
	}
	dependencies := configurationRoutes(t, system)
	t.Cleanup(func() {
		if err := system.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	directory, err := goakt.GrainOf[*sessionregistry.RegistryGrain](ctx, system, sessionregistry.RegistryName, goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	registryDriver, registryReplies := grainDriver(t, system, directory)
	created := request[sessionactor.Initialized](t, registryDriver, registryReplies, sessionregistry.CreateSession{
		SessionID:  "conversation",
		CreationID: "current",
		Storage:    backend,
	})
	if created.Err != nil {
		t.Fatal(created.Err)
	}
	conversation, err := goakt.GrainOf[*sessionactor.Grain](ctx, system, sessionactor.RuntimeName("conversation"), goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	identity, err := goakt.GrainOf[*loopactor.Grain](ctx, system, loopactor.RuntimeName("conversation"), goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	driver, replies := grainDriver(t, system, identity)
	authority, err := goakt.GrainOf[*agentregistry.Grain](ctx, system, agentregistry.Name, goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	authorityDriver, authorityReplies := grainDriver(t, system, authority)
	fault, err := goakt.GrainOf[*closeFailure](ctx, system, "close-failure", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	faultDriver, faultReplies := grainDriver(t, system, fault)
	request[struct{}](t, faultDriver, faultReplies, closeDestination{
		target: directory,
	})

	denied := request[agentactor.Initialized](t, driver, replies, agentactor.Initialize{
		AgentID:    "conversation",
		Authority:  authority,
		CreationID: "current",
		Session:    sessionRoute(t, system, "conversation"),
		Prompt:     dependencies,
	})
	if denied.Err == nil {
		t.Fatal("unreserved Agent initialized")
	}
	initialized := request[agentregistry.Ready](t, authorityDriver, authorityReplies, agentregistry.CreateAgent{
		Creation: agentregistry.Creation{
			AgentID:    "conversation",
			CreationID: "current",
		},
		Storage:         backend,
		SessionRegistry: fault,
		PromptRegistry:  dependencies,
		ModelConfig:     dependencies,
		ToolsRegistry:   dependencies,
		Timeout:         time.Second,
	})
	if initialized.Err != nil {
		t.Fatal(initialized.Err)
	}
	if result := request[agentactor.Closed](t, driver, replies, agentactor.CloseResources{
		CreationID: "current",
	}); result.Err == nil {
		t.Fatal("resources closed before preparation")
	}
	if result := request[agentactor.Prepared](t, driver, replies, agentactor.PrepareClose{
		CreationID: "old",
	}); result.Err == nil {
		t.Fatal("old creation prepared current Agent")
	}

	prompt, err := agentmessage.NewUserMessage(agentmessage.UserMessageInput{
		Content: []agentmessage.ContentBlock{agentmessage.NewTextBlock("pending work")},
		Source:  agentmessage.UserMessageSource{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result := request[loopactor.Changed](t, driver, replies, loopactor.Inject{
		CreationID: "current",
		Message:    prompt,
	}); result.Err != nil {
		t.Fatal(result.Err)
	}
	if result := request[agentactor.Prepared](t, driver, replies, agentactor.PrepareClose{
		CreationID: "current",
	}); !errors.Is(result.Err, agentloop.ErrPending) {
		t.Fatalf("prepare = %v", result.Err)
	}
	if result := request[agentactor.Closed](t, driver, replies, agentactor.CloseResources{
		CreationID: "current",
	}); !errors.Is(result.Err, agentloop.ErrPending) {
		t.Fatalf("close = %v", result.Err)
	}
	if result := request[loopactor.Changed](t, driver, replies, loopactor.Inject{
		CreationID: "current",
		Message:    prompt,
	}); !errors.Is(result.Err, agent.ErrClosing) {
		t.Fatalf("late input = %v", result.Err)
	}
	// Cancellation must still commit through the live Session after close admission.
	if result := request[loopactor.Changed](t, driver, replies, loopactor.Remove{
		CreationID: "current",
		MessageID:  prompt.StableID(),
	}); result.Err != nil {
		t.Fatal(result.Err)
	}

	closing := agentregistry.CloseAgent{
		Creation: agentregistry.Creation{
			AgentID:    "conversation",
			CreationID: "current",
		},
	}
	if result := request[agentactor.Closed](t, authorityDriver, authorityReplies, closing); !errors.Is(result.Err, errClose) {
		t.Fatalf("close failure = %v", result.Err)
	}
	if result := request[agentregistry.Ready](t, authorityDriver, authorityReplies, agentregistry.Resolve{
		AgentID: "conversation",
	}); !errors.Is(result.Err, agent.ErrClosing) {
		t.Fatal("failed close published a route", result.Err)
	}
	for range 2 {
		if result := request[agentactor.Closed](t, authorityDriver, authorityReplies, closing); result.Err != nil {
			t.Fatal(result.Err)
		}
	}

	// The Session Registry must actually release the old identity before success.
	recreated := request[sessionactor.Initialized](t, registryDriver, registryReplies, sessionregistry.ResumeSession{
		SessionID:  "conversation",
		CreationID: "replacement",
		Storage:    backend,
	})
	if recreated.Err != nil {
		t.Fatal(recreated.Err)
	}
	sessionDriver, sessionReplies := grainDriver(t, system, conversation)
	view := request[sessionactor.View](t, sessionDriver, sessionReplies, sessionactor.Read{
		CreationID: "replacement",
	})
	if view.Err != nil {
		t.Fatal(view.Err)
	}
	restored, err := agent.NewInbox(recreated.Header, view.Snapshot.Events)
	if err != nil || restored.HasPending() {
		t.Fatalf("durable cancelled Inbox = %v", err)
	}
	// A duplicate old close must not close the replacement Session.
	if result := request[agentactor.Closed](t, authorityDriver, authorityReplies, closing); result.Err != nil {
		t.Fatal(result.Err)
	}
	view = request[sessionactor.View](t, sessionDriver, sessionReplies, sessionactor.Read{
		CreationID: "replacement",
	})
	if view.Err != nil {
		t.Fatal("duplicate close affected replacement", view.Err)
	}
}

var errClose = errors.New("test: Session close failed")

type closeDestination struct {
	target *goakt.GrainIdentity
}

// closeFailure rejects the first resource close, then forwards retries to the
// real Session Registry so successful replies still prove durable shutdown.
type closeFailure struct {
	target *goakt.GrainIdentity
	failed bool
}

func (*closeFailure) OnActivate(context.Context, *goakt.GrainProps) error   { return nil }
func (*closeFailure) OnDeactivate(context.Context, *goakt.GrainProps) error { return nil }
func (fixture *closeFailure) OnReceive(gc *goakt.GrainContext) {
	switch input := gc.Message().(type) {
	case closeDestination:
		fixture.target = input.target
		gc.Response(struct{}{})
	case sessionregistry.StartSession, sessionregistry.RegisterSession:
		if err := gc.EnableReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.StashNonReentrant))); err != nil {
			panic(err)
		}
		reply := gc.DeferResponse()
		gc.RequestGrain(fixture.target, input).Then(func(payload any, failure error) {
			if failure != nil {
				reply.Response(sessionactor.Initialized{
					Err: failure,
				})
				return
			}
			reply.Response(payload)
		})
	case sessionregistry.CloseSession:
		if !fixture.failed {
			fixture.failed = true
			gc.Response(sessionactor.Closed{
				Err: errClose,
			})
			return
		}
		if err := gc.EnableReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.StashNonReentrant))); err != nil {
			gc.Response(sessionactor.Closed{
				Err: err,
			})
			return
		}
		reply := gc.DeferResponse()
		gc.RequestGrain(fixture.target, input).Then(func(payload any, failure error) {
			if failure != nil {
				reply.Response(sessionactor.Closed{
					Err: failure,
				})
				return
			}
			reply.Response(payload)
		})
	default:
		gc.Unhandled()
	}
}
