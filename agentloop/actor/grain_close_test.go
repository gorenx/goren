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
	"github.com/gorenx/goren/agentloop"
	loopactor "github.com/gorenx/goren/agentloop/actor"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	"github.com/gorenx/goren/session/persistence/sqlite"
	sessionregistry "github.com/gorenx/goren/session/registry"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
)

func TestRegistryClosesDescendantsBeforeParentAndRetriesPendingChild(t *testing.T) {
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
	system, err := goakt.NewActorSystem("agent-tree-close", goakt.WithLogger(log.DiscardLogger))
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
	authority, err := goakt.GrainOf[*agentregistry.Grain](ctx, system, agentregistry.Name, goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	authorityDriver, authorityReplies := grainDriver(t, system, authority)
	directory, err := goakt.GrainOf[*sessionregistry.RegistryGrain](ctx, system, sessionregistry.RegistryName, goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	directoryDriver, directoryReplies := grainDriver(t, system, directory)
	var parent *agentregistry.Creation
	drivers := make(map[session.SessionID]*goakt.PID)
	replies := make(map[session.SessionID]<-chan response)
	for _, identifier := range []session.SessionID{"parent", "child", "grandchild"} {
		creation := agentregistry.Creation{
			AgentID:    identifier,
			CreationID: "original",
		}
		created := request[sessionactor.Initialized](t, directoryDriver, directoryReplies, sessionregistry.CreateSession{
			SessionID:  identifier,
			CreationID: creation.CreationID,
			Storage:    backend,
		})
		if created.Err != nil {
			t.Fatal(created.Err)
		}
		execution, err := goakt.GrainOf[*loopactor.Grain](ctx, system, loopactor.RuntimeName(string(identifier)), goakt.WithLongLivedGrain())
		if err != nil {
			t.Fatal(err)
		}
		drivers[identifier], replies[identifier] = grainDriver(t, system, execution)
		if result := request[agentregistry.Ready](t, authorityDriver, authorityReplies, agentregistry.CreateAgent{
			Creation:        creation,
			Parent:          parent,
			Storage:         backend,
			SessionRegistry: directory,
			PromptRegistry:  dependencies,
			ModelConfig:     dependencies,
			ToolsRegistry:   dependencies,
			Timeout:         time.Second,
		}); result.Err != nil {
			t.Fatal(result.Err)
		}

		parent = &creation
	}
	prompt, err := agentmessage.NewUserMessage(agentmessage.UserMessageInput{
		Content: []agentmessage.ContentBlock{agentmessage.NewTextBlock("unfinished")},
		Source:  agentmessage.UserMessageSource{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result := request[loopactor.Changed](t, drivers["grandchild"], replies["grandchild"], loopactor.Inject{
		CreationID: "original",
		Message:    prompt,
	}); result.Err != nil {
		t.Fatal(result.Err)
	}
	root := agentregistry.Creation{
		AgentID:    "parent",
		CreationID: "original",
	}
	if result := request[agentactor.Closed](t, authorityDriver, authorityReplies, agentregistry.CloseAgent{
		Creation: root,
	}); !errors.Is(result.Err, agentloop.ErrPending) {
		t.Fatalf("close with pending descendant = %v", result.Err)
	}
	for _, identifier := range []session.SessionID{"parent", "child"} {
		if result := request[loopactor.Activity](t, drivers[identifier], replies[identifier], loopactor.ReadActivity{
			CreationID: "original",
		}); result.Err != nil {
			t.Fatal("ancestor released before descendant", identifier, result.Err)
		}
	}
	if result := request[agentregistry.Completion](t, authorityDriver, authorityReplies, agentregistry.Reserve{
		Creation: agentregistry.Creation{
			AgentID:    "late",
			CreationID: "late",
		},
		Parent: parent,
	}); !errors.Is(result.Err, agent.ErrClosing) {
		t.Fatalf("late descendant = %v", result.Err)
	}
	if result := request[loopactor.Changed](t, drivers["grandchild"], replies["grandchild"], loopactor.Remove{
		CreationID: "original",
		MessageID:  prompt.StableID(),
	}); result.Err != nil {
		t.Fatal(result.Err)
	}
	// Independently closing and replacing a descendant must not redirect the
	// parent's retry to the new creation that reused its logical identity.
	if result := request[agentactor.Closed](t, authorityDriver, authorityReplies, agentregistry.CloseAgent{
		Creation: *parent,
	}); result.Err != nil {
		t.Fatal(result.Err)
	}
	replacement := agentregistry.Creation{
		AgentID:    "grandchild",
		CreationID: "replacement",
	}
	if result := request[agentregistry.Completion](t, authorityDriver, authorityReplies, agentregistry.Reserve{
		Creation: replacement,
	}); result.Err != nil {
		t.Fatal(result.Err)
	}
	for range 2 {
		if result := request[agentactor.Closed](t, authorityDriver, authorityReplies, agentregistry.CloseAgent{
			Creation: root,
		}); result.Err != nil {
			t.Fatal(result.Err)
		}
	}
	if result := request[agentregistry.Completion](t, authorityDriver, authorityReplies, agentregistry.CheckAdmission{
		Creation: replacement,
	}); result.Err != nil {
		t.Fatal("parent retry affected replacement", result.Err)
	}
	sessions := request[sessionregistry.Sessions](t, directoryDriver, directoryReplies, sessionregistry.ListSessions{})
	if len(sessions.Items) != 0 {
		t.Fatalf("Sessions remain after close: %+v", sessions.Items)
	}

	if result := request[agentregistry.Completion](t, authorityDriver, authorityReplies, agentregistry.Reserve{
		Creation: root,
	}); !errors.Is(result.Err, agentregistry.ErrCreation) {
		t.Fatalf("reused closed creation = %v", result.Err)
	}
	if result := request[agentregistry.Completion](t, authorityDriver, authorityReplies, agentregistry.Reserve{
		Creation: agentregistry.Creation{
			AgentID:    "parent",
			CreationID: "replacement",
		},
	}); result.Err != nil {
		t.Fatal("parent identity was not released", result.Err)
	}
	if result := request[agentactor.Closed](t, authorityDriver, authorityReplies, agentregistry.CloseAgent{
		Creation: root,
	}); !errors.Is(result.Err, agentregistry.ErrCreation) {
		t.Fatalf("old parent close = %v", result.Err)
	}
}

func TestRegistryClosesAuthorizedCreationAfterConfigurationFails(t *testing.T) {
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
	system, err := goakt.NewActorSystem("agent-tree-close", goakt.WithLogger(log.DiscardLogger))
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
	authority, err := goakt.GrainOf[*agentregistry.Grain](ctx, system, agentregistry.Name, goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	authorityDriver, authorityReplies := grainDriver(t, system, authority)
	directory, err := goakt.GrainOf[*sessionregistry.RegistryGrain](ctx, system, sessionregistry.RegistryName, goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	directoryDriver, directoryReplies := grainDriver(t, system, directory)

	creation := agentregistry.Creation{
		AgentID:    "failed",
		CreationID: "original",
	}
	created := request[sessionactor.Initialized](t, directoryDriver, directoryReplies, sessionregistry.CreateSession{
		SessionID:  creation.AgentID,
		CreationID: creation.CreationID,
		Storage:    backend,
	})
	if created.Err != nil {
		t.Fatal(created.Err)
	}
	conversation, err := goakt.GrainOf[*sessionactor.Grain](ctx, system, sessionactor.RuntimeName(string(creation.AgentID)), goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}

	prompt, err := agentmessage.NewUserMessage(agentmessage.UserMessageInput{
		Content: []agentmessage.ContentBlock{agentmessage.NewTextBlock("preserve pending input")},
		Source:  agentmessage.UserMessageSource{},
	})
	if err != nil {
		t.Fatal(err)
	}
	inbox, err := agent.NewInbox(created.Header, nil)
	if err != nil {
		t.Fatal(err)
	}
	edit, err := inbox.PrepareAppend(agent.NextTurn, prompt)
	if err != nil {
		t.Fatal(err)
	}
	sessionDriver, sessionReplies := grainDriver(t, system, conversation)
	committed := request[sessionactor.Committed](t, sessionDriver, sessionReplies, sessionactor.Commit{
		CreationID: creation.CreationID,
		Plan:       session.Batch(edit.Drafts...),
	})
	if committed.Err != nil {
		t.Fatal(committed.Err)
	}
	// LLM initialization fails before executor activation; Session facts survive cleanup.
	configDriver, configReplies := grainDriver(t, system, dependencies)
	request[struct{}](t, configDriver, configReplies, failConfiguration{})
	if result := request[agentregistry.Ready](t, authorityDriver, authorityReplies, agentregistry.CreateAgent{
		Creation:        creation,
		Storage:         backend,
		SessionRegistry: directory,
		PromptRegistry:  dependencies,
		ModelConfig:     dependencies,
		ToolsRegistry:   dependencies,
		Timeout:         time.Second,
	}); result.Err == nil {
		t.Fatal("failed configuration initialized Agent")
	}

	if result := request[agentactor.Closed](t, authorityDriver, authorityReplies, agentregistry.CloseAgent{
		Creation: creation,
	}); result.Err != nil {
		t.Fatal("failed creation could not close", result.Err)
	}
	sessions := request[sessionregistry.Sessions](t, directoryDriver, directoryReplies, sessionregistry.ListSessions{})
	if len(sessions.Items) != 0 {
		t.Fatalf("failed creation retained Session: %+v", sessions.Items)
	}
	if result := request[agentregistry.Completion](t, authorityDriver, authorityReplies, agentregistry.Reserve{
		Creation: agentregistry.Creation{
			AgentID:    creation.AgentID,
			CreationID: "replacement",
		},
	}); result.Err != nil {
		t.Fatal("failed creation retained Agent identity", result.Err)
	}
	resumed := request[sessionactor.Initialized](t, directoryDriver, directoryReplies, sessionregistry.ResumeSession{
		SessionID:  creation.AgentID,
		CreationID: "replacement",
		Storage:    backend,
	})
	if resumed.Err != nil {
		t.Fatal("failed creation lost durable Session", resumed.Err)
	}
	view := request[sessionactor.View](t, sessionDriver, sessionReplies, sessionactor.Read{
		CreationID: "replacement",
	})
	if view.Err != nil {
		t.Fatal(view.Err)
	}
	restored, err := agent.NewInbox(resumed.Header, view.Snapshot.Events)
	if err != nil {
		t.Fatal(err)
	}
	pending := restored.NextTurn()
	if len(pending) != 1 || pending[0].StableID() != prompt.StableID() {
		t.Fatal("failed initialization cleanup lost pending input")
	}

}
