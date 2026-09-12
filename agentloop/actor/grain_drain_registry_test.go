package loopactor_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorenx/goren/agent"
	agentactor "github.com/gorenx/goren/agent/actor"
	agentregistry "github.com/gorenx/goren/agent/registry"
	loopactor "github.com/gorenx/goren/agentloop/actor"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session/persistence/sqlite"
	sessionregistry "github.com/gorenx/goren/session/registry"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
)

func TestDrainedCompanionClosesAndRestoresPendingInputWithoutClosingParent(t *testing.T) {
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
	system, err := goakt.NewActorSystem("drain-companion", goakt.WithLogger(log.DiscardLogger), goakt.WithPubSub())
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
	dependencies := configurationRoutes(t, system)
	routes := executionDependencies(t, system)
	authority, err := goakt.GrainOf[*agentregistry.Grain](ctx, system, agentregistry.Name, goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	driver, replies := grainDriver(t, system, authority)
	directory, err := goakt.GrainOf[*sessionregistry.RegistryGrain](ctx, system, sessionregistry.RegistryName, goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	parent := agentregistry.Creation{
		AgentID:    "parent",
		CreationID: "parent-creation",
	}
	ready := request[agentregistry.Ready](t, driver, replies, agentregistry.CreateAgent{
		Creation:        parent,
		Storage:         backend,
		SessionRegistry: directory,
		ModelConfig:     dependencies,
		ModelRouter:     routes.router,
		Middleware:      routes.middleware,
		PromptRegistry:  dependencies,
		ToolsRegistry:   toolRegistry(t, system),
		Timeout:         time.Second,
	})
	if ready.Err != nil {
		t.Fatal(ready.Err)
	}
	child := agentregistry.Creation{
		AgentID:    "companion",
		CreationID: "first",
	}
	ready = request[agentregistry.Ready](t, driver, replies, agentregistry.CreateChildAgent{
		Role:   agent.CompanionRole,
		Parent: parent,
		Child:  child,
	})
	if ready.Err != nil {
		t.Fatal(ready.Err)
	}
	target, targetReplies := grainDriver(t, system, ready.Executor)
	queued, err := agentmessage.NewUserMessage(agentmessage.UserMessageInput{
		Content: []agentmessage.ContentBlock{agentmessage.NewTextBlock("retain pending task")},
		Source:  agentmessage.UserMessageSource{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result := request[loopactor.Changed](t, target, targetReplies, loopactor.Inject{
		CreationID: child.CreationID,
		Message:    queued,
	}); result.Err != nil {
		t.Fatal(result.Err)
	}
	if result := request[agentactor.Prepared](t, target, targetReplies, agentactor.PrepareClose{
		CreationID:    child.CreationID,
		FinishCurrent: true,
	}); result.Err != nil {
		t.Fatal(result.Err)
	}
	if result := request[loopactor.Changed](t, target, targetReplies, loopactor.Followup{
		CreationID: child.CreationID,
		Message:    queued,
	}); !errors.Is(result.Err, agent.ErrClosing) {
		t.Fatalf("drained child accepted input: %v", result.Err)
	}
	if result := request[agentactor.Closed](t, driver, replies, agentregistry.CloseAgent{
		Creation: child,
	}); result.Err != nil {
		t.Fatal(result.Err)
	}
	stillReady := request[agentregistry.Ready](t, driver, replies, agentregistry.FindAgent{
		AgentID: parent.AgentID,
	})
	if stillReady.Err != nil || stillReady.Creation != parent {
		t.Fatalf("child close affected parent: %+v", stillReady)
	}
	child.CreationID = "restored"
	ready = request[agentregistry.Ready](t, driver, replies, agentregistry.CreateChildAgent{
		Role:    agent.CompanionRole,
		Parent:  parent,
		Child:   child,
		Restore: true,
	})
	if ready.Err != nil {
		t.Fatal(ready.Err)
	}
	if ready.Registration.Role != agent.CompanionRole {
		t.Fatal("restoration lost companion role")
	}
	activity := request[loopactor.Activity](t, target, targetReplies, loopactor.ReadActivity{
		CreationID: child.CreationID,
	})
	if activity.Err != nil || len(activity.Pending.NextStep) != 1 || activity.Pending.NextStep[0].StableID() != queued.StableID() {
		t.Fatalf("restoration lost pending input: %+v", activity)
	}
	if result := request[agentactor.Prepared](t, target, targetReplies, agentactor.PrepareClose{
		CreationID:    "first",
		FinishCurrent: true,
	}); result.Err == nil {
		t.Fatal("old close affected replacement")
	}
	if result := request[agentactor.Prepared](t, target, targetReplies, agentactor.PrepareClose{
		CreationID:    child.CreationID,
		FinishCurrent: true,
	}); result.Err != nil {
		t.Fatal(result.Err)
	}
	if result := request[agentactor.Closed](t, driver, replies, agentregistry.CloseAgent{
		Creation: parent,
	}); result.Err != nil {
		t.Fatal(result.Err)
	}
}
