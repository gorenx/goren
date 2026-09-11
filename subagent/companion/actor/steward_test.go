package companionactor

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gorenx/goren/agent"
	agentactor "github.com/gorenx/goren/agent/actor"
	middlewareactor "github.com/gorenx/goren/agent/middleware/actor"
	agentregistry "github.com/gorenx/goren/agent/registry"
	loopactor "github.com/gorenx/goren/agentloop/actor"
	"github.com/gorenx/goren/agentmessage"
	llmactor "github.com/gorenx/goren/llm/actor"
	"github.com/gorenx/goren/session/persistence/sqlite"
	sessionregistry "github.com/gorenx/goren/session/registry"
	"github.com/gorenx/goren/subagent/companion"
	"github.com/gorenx/goren/systemprompt"
	promptactor "github.com/gorenx/goren/systemprompt/actor"
	toolregistry "github.com/gorenx/goren/tools/registry"
	toolactor "github.com/gorenx/goren/tools/registry/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
	"github.com/tochemey/goakt/v4/reentrancy"
)

// configurationFixture supplies config only; execution, Registry, Session and
// SQLite remain real. Configuration independence is verified in a later stage.
type configurationFixture struct{}

func (*configurationFixture) OnActivate(context.Context, *goakt.GrainProps) error   { return nil }
func (*configurationFixture) OnDeactivate(context.Context, *goakt.GrainProps) error { return nil }
func (*configurationFixture) OnReceive(gc *goakt.GrainContext) {
	switch gc.Message().(type) {
	case llmactor.InitializeAgent:
		gc.Response(llmactor.Completion{})
	case agentactor.CancelMaintenance:
		gc.Response(agentactor.MaintenanceCancelled{})
	case promptactor.Prepare:
		selected, err := systemprompt.ValidateConfig(systemprompt.Config{})
		if err != nil {
			panic(err)
		}
		layer, err := systemprompt.NewLayer(selected)
		if err != nil {
			panic(err)
		}
		gc.Response(promptactor.Prepared{
			Version:  1,
			Snapshot: layer.Capture(),
		})
	default:
		gc.Unhandled()
	}
}

type creationGate struct {
	entered  chan struct{}
	canceled chan struct{}
	release  chan struct{}
}

type childFixture struct {
	system   goakt.ActorSystem
	registry *goakt.GrainIdentity
	storage  *sqlite.Adapter
	mu       sync.Mutex
	gate     *creationGate
	failure  error
}

func (fixture *childFixture) Start(ctx context.Context, parent, child agentregistry.Creation, initial companion.Config) (agentregistry.Ready, error) {
	fixture.mu.Lock()
	gate := fixture.gate
	failure := fixture.failure
	fixture.gate = nil
	fixture.mu.Unlock()
	if gate != nil {
		close(gate.entered)
		select {
		case <-ctx.Done():
			close(gate.canceled)
			<-gate.release
		case <-gate.release:
		}
	}
	if failure != nil {
		return agentregistry.Ready{}, failure
	}
	// This fixture deliberately supports only an empty, independent tool setup.
	if len(initial.Tools) != 0 || len(initial.MCP) != 0 || initial.Model.Model != "" || initial.Prompt.Persona != "" {
		return agentregistry.Ready{}, errors.New("test: configuration outside lifecycle fixture scope")
	}
	stored, err := fixture.storage.Load(context.Background(), child.AgentID)
	if err != nil {
		return agentregistry.Ready{}, err
	}
	return ask[agentregistry.Ready](context.Background(), fixture.system, fixture.registry, agentregistry.CreateChildAgent{
		Role:    agent.CompanionRole,
		Parent:  parent,
		Child:   child,
		Restore: stored != nil,
		Tools: &toolregistry.Catalog{
			InheritGlobal: false,
		},
	})
}

type stewardship struct {
	system        goakt.ActorSystem
	authority     *goakt.GrainIdentity
	sessions      *goakt.GrainIdentity
	configuration *goakt.GrainIdentity
	input         agentregistry.CreateAgent
	creator       *childFixture
	client        *Client
}

func request[T any](t *testing.T, system goakt.ActorSystem, target *goakt.GrainIdentity, input any) T {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	result, err := ask[T](ctx, system, target, input)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func newStewardship(t *testing.T) *stewardship {
	t.Helper()
	storage, err := sqlite.Open(t.Context(), sqlite.Config{
		Path:        filepath.Join(t.TempDir(), "sessions.sqlite"),
		JournalMode: sqlite.JournalWAL,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := storage.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	system, err := goakt.NewActorSystem("steward", goakt.WithLogger(log.DiscardLogger), goakt.WithPubSub())
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
	authority, err := goakt.GrainOf[*agentregistry.Grain](t.Context(), system, agentregistry.Name, goakt.WithLongLivedGrain(), goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := goakt.GrainOf[*sessionregistry.RegistryGrain](t.Context(), system, sessionregistry.RegistryName, goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := goakt.GrainOf[*configurationFixture](t.Context(), system, "configuration", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	tools, err := goakt.GrainOf[*toolactor.RegistryGrain](t.Context(), system, toolactor.RegistryName, goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	if result := request[toolactor.Completion](t, system, tools, toolactor.RegisterSource{
		Name: "local",
	}); result.Err != nil {
		t.Fatal(result.Err)
	}
	pool, err := llmactor.SpawnPool(t.Context(), system, "models", 1, llmactor.InitializeWorker{
		LLM: configuration,
	})
	if err != nil {
		t.Fatal(err)
	}
	hooks, err := goakt.GrainOf[*middlewareactor.Grain](t.Context(), system, "middleware", goakt.WithLongLivedGrain(), goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		t.Fatal(err)
	}
	fixture := &stewardship{
		system:        system,
		authority:     authority,
		sessions:      sessions,
		configuration: configuration,
		input: agentregistry.CreateAgent{
			Creation: agentregistry.Creation{
				AgentID:    "parent",
				CreationID: "first",
			},
			Storage:         storage,
			SessionRegistry: sessions,
			ModelConfig:     configuration,
			ModelRouter:     pool,
			Middleware:      hooks,
			PromptRegistry:  configuration,
			ToolsRegistry:   tools,
			Timeout:         time.Second,
		},
		creator: &childFixture{
			system:   system,
			registry: authority,
			storage:  storage,
		},
	}
	fixture.open(t)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := fixture.client.Close(ctx); err != nil {
			t.Error(err)
		}
		result, err := ask[agentactor.Closed](ctx, system, authority, agentregistry.CloseAgent{
			Creation: fixture.input.Creation,
		})
		if err != nil || result.Err != nil {
			t.Errorf("parent cleanup: %v %v", err, result.Err)
		}
	})
	return fixture
}

func (fixture *stewardship) open(t *testing.T) {
	t.Helper()
	ready := request[agentregistry.Ready](t, fixture.system, fixture.authority, fixture.input)
	if ready.Err != nil {
		t.Fatal(ready.Err)
	}
	route := request[sessionregistry.Endpoint](t, fixture.system, fixture.sessions, sessionregistry.ResolveSession{
		SessionID:  fixture.input.Creation.AgentID,
		CreationID: fixture.input.Creation.CreationID,
	})
	if route.Err != nil {
		t.Fatal(route.Err)
	}
	endpoint, err := Open(t.Context(), fixture.system, Initialization{
		Parent:   fixture.input.Creation,
		Registry: fixture.authority,
		Session:  route.Target,
		Creator:  fixture.creator,
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.client = endpoint
}

func awaitPhase(t *testing.T, sender *Client, name string, expected Phase) View {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	var observed []View
	for {
		var err error
		observed, err = sender.List(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range observed {
			if entry.Companion.Name == name && entry.Phase == expected {
				return entry
			}
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatalf("%s never reached %s: %+v", name, expected, observed)
		}
	}
}

func TestStewardCreatesDisablesRestartsAndRecoversRemoval(t *testing.T) {
	fixture := newStewardship(t)
	first, err := fixture.client.Create(t.Context(), Create{
		Name: "reviewer",
		Initial: companion.Config{
			Enabled: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ready := awaitPhase(t, fixture.client, "reviewer", Ready)
	if first.Companion.ChildSessionID != ready.Creation.AgentID {
		t.Fatal("child identity changed during startup")
	}
	stored, err := fixture.creator.storage.Load(t.Context(), "parent")
	if err != nil {
		t.Fatal(err)
	}
	records, err := companion.CompanionsFromEvents("parent", stored.Events)
	if err != nil || len(records) != 1 || records[0].ChildSessionID != ready.Creation.AgentID {
		t.Fatal("child started without durable intent")
	}
	if _, err := fixture.client.Create(t.Context(), Create{
		Name: "reviewer",
	}); !errors.Is(err, companion.ErrConflict) {
		t.Fatalf("duplicate creation: %v", err)
	}
	disabled, err := fixture.client.Configure(t.Context(), Configure{
		Name:             "reviewer",
		ExpectedRevision: ready.Companion.Revision,
		Enabled:          false,
	})
	if err != nil {
		t.Fatal(err)
	}
	awaitPhase(t, fixture.client, "reviewer", Stopped)
	if _, err := fixture.client.Configure(t.Context(), Configure{
		Name:             "reviewer",
		ExpectedRevision: ready.Companion.Revision,
		Enabled:          true,
	}); !errors.Is(err, companion.ErrConflict) {
		t.Fatalf("stale version accepted: %v", err)
	}
	if _, err := fixture.client.Configure(t.Context(), Configure{
		Name:             "reviewer",
		ExpectedRevision: disabled.Companion.Revision,
		Enabled:          true,
	}); err != nil {
		t.Fatal(err)
	}
	resumed := awaitPhase(t, fixture.client, "reviewer", Ready)
	if resumed.Creation.AgentID != ready.Creation.AgentID || resumed.Creation.CreationID == ready.Creation.CreationID {
		t.Fatal("enable did not restore same Session with a new lifetime")
	}
	if _, err := fixture.client.Restart(t.Context(), Restart{
		Name:               "reviewer",
		ExpectedRevision:   resumed.Companion.Revision,
		ExpectedCreationID: resumed.Creation.CreationID,
	}); err != nil {
		t.Fatal(err)
	}
	restarted := awaitPhase(t, fixture.client, "reviewer", Ready)
	if restarted.Creation.CreationID == resumed.Creation.CreationID {
		t.Fatal("restart reused creation")
	}
	if _, err := fixture.client.Remove(t.Context(), Remove{
		Name:             "reviewer",
		ExpectedRevision: restarted.Companion.Revision,
	}); err != nil {
		t.Fatal(err)
	}
	awaitPhase(t, fixture.client, "reviewer", Stopped)
	old := fixture.client
	if err := old.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if closed := request[agentactor.Closed](t, fixture.system, fixture.authority, agentregistry.CloseAgent{
		Creation: fixture.input.Creation,
	}); closed.Err != nil {
		t.Fatal(closed.Err)
	}
	fixture.input.Creation.CreationID = "second"
	fixture.input.Restore = true
	fixture.open(t)
	items, err := fixture.client.List(t.Context())
	if err != nil || len(items) != 1 || !items[0].Companion.Removed || items[0].Creation.CreationID != "" {
		t.Fatalf("removed Companion resurrected: %+v %v", items, err)
	}
	if _, err := old.List(t.Context()); !errors.Is(err, agentregistry.ErrCreation) {
		t.Fatalf("stale Client retained access: %v", err)
	}
	if _, err := fixture.client.Create(t.Context(), Create{
		Name: "reviewer",
		Initial: companion.Config{
			Enabled: true,
		},
	}); err != nil {
		t.Fatal(err)
	}
	replacement := awaitPhase(t, fixture.client, "reviewer", Ready)
	if replacement.Creation.AgentID == ready.Creation.AgentID {
		t.Fatal("recreation reused removed child Session")
	}
}

func TestStewardRemovalWaitsForBusyChildAndRejectsNewInput(t *testing.T) {
	fixture := newStewardship(t)
	if _, err := fixture.client.Create(t.Context(), Create{
		Name: "busy",
		Initial: companion.Config{
			Enabled: true,
		},
	}); err != nil {
		t.Fatal(err)
	}
	ready := awaitPhase(t, fixture.client, "busy", Ready)
	child := request[agentregistry.Ready](t, fixture.system, fixture.authority, agentregistry.FindAgent{
		AgentID: ready.Creation.AgentID,
	})
	if result := request[agentactor.MaintenanceAccepted](t, fixture.system, child.Executor, agentactor.BeginMaintenance{
		CreationID: ready.Creation.CreationID,
		RequestID:  "current-work",
		Worker:     fixture.configuration,
	}); result.Err != nil {
		t.Fatal(result.Err)
	}
	if _, err := fixture.client.Remove(t.Context(), Remove{
		Name:             "busy",
		ExpectedRevision: ready.Companion.Revision,
	}); err != nil {
		t.Fatal(err)
	}
	awaitPhase(t, fixture.client, "busy", Draining)
	// The Agent processes the preparation request before acknowledging this read.
	input, err := agentmessage.NewUserMessage(agentmessage.UserMessageInput{
		Content: []agentmessage.ContentBlock{agentmessage.NewTextBlock("new task")},
		Source:  agentmessage.UserMessageSource{},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Wait for the actual input owner to observe closing, rather than assuming a
	// management phase implies that its asynchronous request already arrived.
	for {
		result := request[agentactor.MaintenanceAccepted](t, fixture.system, child.Executor, agentactor.BeginMaintenance{
			CreationID: ready.Creation.CreationID,
			RequestID:  "probe",
			Worker:     fixture.configuration,
		})
		if errors.Is(result.Err, agent.ErrClosing) {
			break
		}
		if !errors.Is(result.Err, agent.ErrBusy) {
			t.Fatalf("unexpected admission: %v", result.Err)
		}
	}
	if result := request[loopactor.Changed](t, fixture.system, child.Executor, loopactor.Followup{
		CreationID: ready.Creation.CreationID,
		Message:    input,
	}); !errors.Is(result.Err, agent.ErrClosing) {
		t.Fatalf("draining accepted input: %v", result.Err)
	}
	activity := request[loopactor.Activity](t, fixture.system, child.Executor, loopactor.ReadActivity{
		CreationID: ready.Creation.CreationID,
	})
	if activity.Status != agent.StatusRunning {
		t.Fatal("work ended before its owner completed")
	}
	if result := request[agentactor.MaintenanceFinished](t, fixture.system, child.Executor, agentactor.FinishMaintenance{
		CreationID: ready.Creation.CreationID,
		RequestID:  "current-work",
	}); result.Err != nil {
		t.Fatal(result.Err)
	}
	awaitPhase(t, fixture.client, "busy", Stopped)
}

func TestStewardSettlesLateCreationAfterRemoveOrClose(t *testing.T) {
	for _, shutdownParent := range []bool{false, true} {
		label := "remove"
		if shutdownParent {
			label = "close"
		}
		t.Run(label, func(t *testing.T) {
			fixture := newStewardship(t)
			gate := &creationGate{
				entered:  make(chan struct{}),
				canceled: make(chan struct{}),
				release:  make(chan struct{}),
			}
			defer func() {
				select {
				case <-gate.release:
				default:
					close(gate.release)
				}
			}()
			fixture.creator.mu.Lock()
			fixture.creator.gate = gate
			fixture.creator.mu.Unlock()
			created, err := fixture.client.Create(t.Context(), Create{
				Name: "late",
				Initial: companion.Config{
					Enabled: true,
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-gate.entered:
			case <-time.After(time.Second):
				t.Fatal("startup did not begin")
			}
			completed := make(chan error, 1)
			if shutdownParent {
				go func() { completed <- fixture.client.Close(t.Context()) }()
			} else {
				_, err = fixture.client.Remove(t.Context(), Remove{
					Name:             "late",
					ExpectedRevision: created.Companion.Revision,
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			select {
			case <-gate.canceled:
			case <-time.After(time.Second):
				t.Fatal("pending startup was not cancelled")
			}
			if shutdownParent {
				select {
				case failure := <-completed:
					t.Fatalf("close forgot outstanding creation: %v", failure)
				default:
				}
			}
			close(gate.release)
			if shutdownParent {
				select {
				case failure := <-completed:
					if failure != nil {
						t.Fatal(failure)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("close did not settle late creation")
				}
			}
			awaitPhase(t, fixture.client, "late", Stopped)
			found := request[agentregistry.Ready](t, fixture.system, fixture.authority, agentregistry.FindAgent{
				AgentID: created.Companion.ChildSessionID,
			})
			if found.Err == nil {
				t.Fatal("late creation remained ready")
			}
			parent := request[agentregistry.Ready](t, fixture.system, fixture.authority, agentregistry.FindAgent{
				AgentID: fixture.input.Creation.AgentID,
			})
			if parent.Err != nil {
				t.Fatal("Steward closed parent", parent.Err)
			}
		})
	}
}

func TestStewardFailedStartupCanRestartAndCompanionCannotOwnSteward(t *testing.T) {
	fixture := newStewardship(t)
	fixture.creator.mu.Lock()
	fixture.creator.failure = errors.New("injected preparation failure")
	fixture.creator.mu.Unlock()
	if _, err := fixture.client.Create(t.Context(), Create{
		Name: "retry",
		Initial: companion.Config{
			Enabled: true,
		},
	}); err != nil {
		t.Fatal(err)
	}
	failed := awaitPhase(t, fixture.client, "retry", StartFailed)
	// The failed creation is removed asynchronously before restart admission.
	for failed.Creation.CreationID != "" {
		failed = awaitPhase(t, fixture.client, "retry", StartFailed)
	}
	if failed.Err == nil {
		t.Fatal("startup failure disappeared")
	}
	fixture.creator.mu.Lock()
	fixture.creator.failure = nil
	fixture.creator.mu.Unlock()
	if _, err := fixture.client.Restart(t.Context(), Restart{
		Name:             "retry",
		ExpectedRevision: failed.Companion.Revision,
	}); err != nil {
		t.Fatal(err)
	}
	ready := awaitPhase(t, fixture.client, "retry", Ready)
	if ready.Companion.ChildSessionID != failed.Companion.ChildSessionID {
		t.Fatal("retry replaced durable child identity")
	}
	route := request[sessionregistry.Endpoint](t, fixture.system, fixture.sessions, sessionregistry.ResolveSession{
		SessionID:  ready.Creation.AgentID,
		CreationID: ready.Creation.CreationID,
	})
	if _, err := Open(t.Context(), fixture.system, Initialization{
		Parent:   ready.Creation,
		Registry: fixture.authority,
		Session:  route.Target,
		Creator:  fixture.creator,
	}); !errors.Is(err, agent.ErrRole) {
		t.Fatalf("recursive Steward accepted: %v", err)
	}
}

func TestStewardReenableSettlesCancelledStartupBeforeNewCreation(t *testing.T) {
	fixture := newStewardship(t)
	gate := &creationGate{
		entered:  make(chan struct{}),
		canceled: make(chan struct{}),
		release:  make(chan struct{}),
	}
	defer func() {
		select {
		case <-gate.release:
		default:
			close(gate.release)
		}
	}()
	fixture.creator.mu.Lock()
	fixture.creator.gate = gate
	fixture.creator.mu.Unlock()
	created, err := fixture.client.Create(t.Context(), Create{
		Name: "reenable",
		Initial: companion.Config{
			Enabled: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-gate.entered:
	case <-time.After(time.Second):
		t.Fatal("startup did not begin")
	}
	disabled, err := fixture.client.Configure(t.Context(), Configure{
		Name:             "reenable",
		ExpectedRevision: created.Companion.Revision,
		Enabled:          false,
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-gate.canceled:
	case <-time.After(time.Second):
		t.Fatal("startup was not cancelled")
	}
	enabled, err := fixture.client.Configure(t.Context(), Configure{
		Name:             "reenable",
		ExpectedRevision: disabled.Companion.Revision,
		Enabled:          true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if enabled.Phase != Stopping || enabled.Creation != created.Creation {
		t.Fatalf("reenable abandoned the cancelled creation: %+v", enabled)
	}
	close(gate.release)
	ready := awaitPhase(t, fixture.client, "reenable", Ready)
	if ready.Creation.AgentID != created.Creation.AgentID || ready.Creation.CreationID == created.Creation.CreationID {
		t.Fatalf("reenable reused cancelled lifetime: before=%+v after=%+v", created, ready)
	}
	obsolete := request[agentregistry.Inspected](t, fixture.system, fixture.authority, agentregistry.Inspect{
		Creation: created.Creation,
	})
	if !errors.Is(obsolete.Err, agentregistry.ErrCreation) {
		t.Fatalf("old creation remained admitted: %v", obsolete.Err)
	}
}
