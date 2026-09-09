package registry_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorenx/goren/agent"
	agentactor "github.com/gorenx/goren/agent/actor"
	agentregistry "github.com/gorenx/goren/agent/registry"
	loopactor "github.com/gorenx/goren/agentloop/actor"
	"github.com/gorenx/goren/agentmessage"
	commandactor "github.com/gorenx/goren/commands/actor"
	compactionregistry "github.com/gorenx/goren/compaction/basic/registry"
	configactor "github.com/gorenx/goren/llm/actor"
	sessionactor "github.com/gorenx/goren/session/actor"
	"github.com/gorenx/goren/session/persistence/sqlite"
	sessionregistry "github.com/gorenx/goren/session/registry"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
	"github.com/tochemey/goakt/v4/reentrancy"
)

func askLifecycle[T any](t *testing.T, system goakt.ActorSystem, target *goakt.GrainIdentity, input any) T {
	t.Helper()
	payload, err := system.AskGrain(context.Background(), target, input, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	value, ok := payload.(T)
	if !ok {
		t.Fatalf("unexpected reply %T", payload)
	}
	return value
}

func creationFixture(t *testing.T, options ...goakt.Option) (goakt.ActorSystem, *goakt.GrainIdentity, agentregistry.CreateAgent) {
	t.Helper()
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
	system, err := goakt.NewActorSystem("agent-creation", append([]goakt.Option{goakt.WithLogger(log.DiscardLogger)}, options...)...)
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
	authority, err := goakt.GrainOf[*agentregistry.Grain](ctx, system, agentregistry.Name, goakt.WithLongLivedGrain(), goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := goakt.GrainOf[*sessionregistry.RegistryGrain](ctx, system, sessionregistry.RegistryName, goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	directory, err := goakt.GrainOf[*commandactor.Registry](ctx, system, commandactor.RegistryName, goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	dependencies := configurationRoutes(t, system)
	return system, authority, agentregistry.CreateAgent{
		Creation: agentregistry.Creation{
			AgentID:    "conversation",
			CreationID: "first",
		},
		Storage:         backend,
		SessionRegistry: sessions,
		ModelConfig:     dependencies,
		PromptRegistry:  dependencies,
		ToolsRegistry:   dependencies,
		Commands:        directory,
		Timeout:         time.Second,
	}
}

func TestCreationConcurrentCallersShareLifetimeAndColdResume(t *testing.T) {
	system, authority, input := creationFixture(t)
	results := make(chan response, 8)
	for range 8 {
		go func() {
			payload, err := system.AskGrain(context.Background(), authority, input, 3*time.Second)
			results <- response{
				value: payload,
				err:   err,
			}
		}()
	}
	var first agentregistry.Ready
	for range 8 {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		current := result.value.(agentregistry.Ready)
		if current.Err != nil {
			t.Fatal(current.Err)
		}
		if first.Executor == nil {
			first = current
		}
		if !first.Executor.Equal(current.Executor) || !first.Commands.Equal(current.Commands) || current.Creation != input.Creation {
			t.Fatal("duplicate startup published a different lifetime")
		}
	}
	if first.Executor.Name() != "conversation.agentloop" || first.Commands.Name() != "conversation.commands" || first.Executor.Kind() == first.Commands.Kind() {
		t.Fatal("framework identity does not distinguish services")
	}
	if result := askLifecycle[agentregistry.Ready](t, system, authority, agentregistry.FindAgent{AgentID: "conversation"}); result.Err != nil {
		t.Fatal(result.Err)
	}
	entry, err := agentmessage.NewUserMessage(agentmessage.UserMessageInput{
		Content: []agentmessage.ContentBlock{agentmessage.NewTextBlock("durable input")},
		Source:  agentmessage.UserMessageSource{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result := askLifecycle[loopactor.Changed](t, system, first.Executor, loopactor.Inject{
		CreationID: "first",
		Message:    entry,
	}); result.Err != nil {
		t.Fatal(result.Err)
	}
	if result := askLifecycle[loopactor.Changed](t, system, first.Executor, loopactor.Remove{
		CreationID: "first",
		MessageID:  entry.StableID(),
	}); result.Err != nil {
		t.Fatal(result.Err)
	}
	if result := askLifecycle[agentactor.Closed](t, system, authority, agentregistry.CloseAgent{Creation: input.Creation}); result.Err != nil {
		t.Fatal(result.Err)
	}
	oldCommands := askLifecycle[commandactor.Executed](t, system, first.Commands, commandactor.Execute{
		CreationID: "first",
		CommandID:  "stale-command",
		Line:       "/compact",
	})
	if oldCommands.Err == nil {
		t.Fatal("stopped command runtime accepted old input")
	}
	input.Creation.CreationID = "second"
	input.Restore = true
	second := askLifecycle[agentregistry.Ready](t, system, authority, input)
	if second.Err != nil {
		t.Fatal(second.Err)
	}
	if result := askLifecycle[agentactor.Closed](t, system, authority, agentregistry.CloseAgent{Creation: first.Creation}); !errors.Is(result.Err, agentregistry.ErrCreation) {
		t.Fatal("old close affected replacement", result.Err)
	}
	if result := askLifecycle[sessionactor.View](t, system, sessionRoute(t, system, "conversation"), sessionactor.Read{CreationID: "second"}); result.Err != nil || result.Header.ID != "conversation" {
		t.Fatal("recovery lost Session", result.Err)
	}
	if result := askLifecycle[agentactor.Closed](t, system, authority, agentregistry.CloseAgent{Creation: second.Creation}); result.Err != nil {
		t.Fatal(result.Err)
	}
}

type holdConfiguration struct {
	replies chan *goakt.GrainReply
}

func TestCloseDuringInitializationWaitsAndDoesNotPublishReady(t *testing.T) {
	system, authority, input := creationFixture(t)
	held := make(chan *goakt.GrainReply, 1)
	askLifecycle[struct{}](t, system, input.ModelConfig, holdConfiguration{replies: held})
	results := make(chan response, 1)
	go func() {
		payload, err := system.AskGrain(context.Background(), authority, input, 3*time.Second)
		results <- response{
			value: payload,
			err:   err,
		}
	}()
	var pending *goakt.GrainReply
	select {
	case pending = <-held:
	case <-time.After(time.Second):
		t.Fatal("initialization did not request configuration")
	}
	if result := askLifecycle[agentregistry.Ready](t, system, authority, agentregistry.FindAgent{AgentID: "conversation"}); result.Err == nil {
		t.Fatal("Claim was mistaken for readiness")
	}
	closed := make(chan response, 1)
	go func() {
		payload, err := system.AskGrain(context.Background(), authority, agentregistry.CloseAgent{Creation: input.Creation}, 3*time.Second)
		closed <- response{
			value: payload,
			err:   err,
		}
	}()
	// BeginClose is an idempotent mailbox barrier; the final close must still wait for initialization.
	if result := askLifecycle[agentregistry.CloseAccepted](t, system, authority, agentregistry.BeginClose{Creation: input.Creation}); result.Err != nil {
		t.Fatal(result.Err)
	}
	select {
	case result := <-closed:
		t.Fatalf("closed before initialization settled: %+v", result)
	default:
	}
	pending.Response(configactor.Completion{})
	result := <-results
	if result.err != nil || !errors.Is(result.value.(agentregistry.Ready).Err, agent.ErrClosing) {
		t.Fatal("closing creation published success", result)
	}
	result = <-closed
	if result.err != nil || result.value.(agentactor.Closed).Err != nil {
		t.Fatal("close did not finish", result)
	}
	sessions := askLifecycle[sessionregistry.Sessions](t, system, input.SessionRegistry, sessionregistry.ListSessions{})
	if len(sessions.Items) != 0 {
		t.Fatal("closed creation retained Session")
	}
}

func TestCreationOwnsCompactionThroughCapabilityRegistry(t *testing.T) {
	system, authority, input := creationFixture(t)
	capability, err := goakt.GrainOf[*compactionregistry.Grain](context.Background(), system, compactionregistry.Name, goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	// No model request is needed to test initialization and withdrawal; a live PID is sufficient.
	router, _ := grainDriver(t, system, input.ModelConfig)
	if result := askLifecycle[compactionregistry.Configured](t, system, capability, compactionregistry.Configure{
		Commands: input.Commands,
		Meter:    input.ModelConfig,
		Router:   router,
		Timeout:  time.Second,
	}); result.Err != nil {
		t.Fatal(result.Err)
	}
	input.Components = []*goakt.GrainIdentity{capability}
	current := askLifecycle[agentregistry.Ready](t, system, authority, input)
	if current.Err != nil || len(current.Components) != 1 || current.Components[0].Runtime == nil {
		t.Fatal("component startup failed", current.Err)
	}
	repeated := askLifecycle[agentactor.ComponentStarted](t, system, capability, agentactor.StartComponent{
		AgentID:    input.Creation.AgentID,
		CreationID: input.Creation.CreationID,
		Agent:      current.Executor,
		Session:    sessionRoute(t, system, "conversation"),
	})
	if repeated.Err != nil || !repeated.Runtime.Equal(current.Components[0].Runtime) {
		t.Fatal("component startup was not idempotent", repeated.Err)
	}
	if result := askLifecycle[agentactor.Closed](t, system, authority, agentregistry.CloseAgent{Creation: input.Creation}); result.Err != nil {
		t.Fatal(result.Err)
	}
	if result := askLifecycle[agentactor.ComponentStopped](t, system, capability, agentactor.StopComponent{
		AgentID:    input.Creation.AgentID,
		CreationID: input.Creation.CreationID,
	}); result.Err != nil {
		t.Fatal(result.Err)
	}
	route := askLifecycle[commandactor.Resolved](t, system, input.Commands, commandactor.Resolve{
		SessionID: input.Creation.AgentID,
		Name:      "compact",
	})
	if route.Handler != nil {
		t.Fatal("closed component retained command route")
	}
	if result := askLifecycle[agentactor.ComponentStarted](t, system, capability, agentactor.StartComponent{
		AgentID:    input.Creation.AgentID,
		CreationID: input.Creation.CreationID,
		Agent:      current.Executor,
		Session:    sessionRoute(t, system, "conversation"),
	}); result.Err == nil {
		t.Fatal("old startup resurrected closed component")
	}
	input.Creation.CreationID = "second"
	input.Restore = false
	replacement := askLifecycle[agentregistry.Ready](t, system, authority, input)
	if replacement.Err != nil {
		t.Fatal(replacement.Err)
	}
	if result := askLifecycle[agentactor.ComponentStopped](t, system, capability, agentactor.StopComponent{
		AgentID:    input.Creation.AgentID,
		CreationID: "first",
	}); result.Err == nil {
		t.Fatal("old component close reached replacement")
	}
	if result := askLifecycle[agentactor.Closed](t, system, authority, agentregistry.CloseAgent{Creation: input.Creation}); result.Err != nil {
		t.Fatal(result.Err)
	}
}

type componentBehavior struct {
	events    chan string
	failStart bool
	failStop  bool
}

type componentOwner struct {
	settings componentBehavior
}

func (*componentOwner) OnActivate(context.Context, *goakt.GrainProps) error   { return nil }
func (*componentOwner) OnDeactivate(context.Context, *goakt.GrainProps) error { return nil }
func (owner *componentOwner) OnReceive(gc *goakt.GrainContext) {
	switch input := gc.Message().(type) {
	case componentBehavior:
		owner.settings = input
		gc.Response(struct{}{})
	case agentactor.StartComponent:
		owner.settings.events <- "start-" + gc.Self().Name()
		if owner.settings.failStart {
			gc.Response(agentactor.ComponentStarted{Err: errors.New("test: component startup failed")})
			return
		}
		gc.Response(agentactor.ComponentStarted{Runtime: input.Agent})
	case agentactor.StopComponent:
		owner.settings.events <- "stop-" + gc.Self().Name()
		if owner.settings.failStop {
			owner.settings.failStop = false
			gc.Response(agentactor.ComponentStopped{Err: errors.New("test: component shutdown failed")})
			return
		}
		gc.Response(agentactor.ComponentStopped{})
	}
}

func TestComponentFailurePreservesSessionAndRetriesReverseShutdown(t *testing.T) {
	for _, failStart := range []bool{false, true} {
		t.Run(fmt.Sprint("startup-failure-", failStart), func(t *testing.T) {
			system, authority, input := creationFixture(t)
			events := make(chan string, 16)
			for _, name := range []string{"first", "second"} {
				route, err := goakt.GrainOf[*componentOwner](context.Background(), system, name, goakt.WithLongLivedGrain())
				if err != nil {
					t.Fatal(err)
				}
				askLifecycle[struct{}](t, system, route, componentBehavior{
					events:    events,
					failStart: failStart && name == "second",
					failStop:  name == "second",
				})
				input.Components = append(input.Components, route)
			}
			current := askLifecycle[agentregistry.Ready](t, system, authority, input)
			if (current.Err != nil) != failStart {
				t.Fatal("unexpected creation result", current.Err)
			}
			if failStart && (current.Executor != nil || current.Commands != nil || len(current.Components) != 0) {
				t.Fatal("failed initialization published routes")
			}
			if got := <-events; got != "start-first" {
				t.Fatal(got)
			}
			if got := <-events; got != "start-second" {
				t.Fatal(got)
			}
			if result := askLifecycle[agentregistry.Ready](t, system, authority, input); (result.Err != nil) != failStart {
				t.Fatal(result.Err)
			}

			conflicting := input
			conflicting.Restore = !input.Restore
			if result := askLifecycle[agentregistry.Ready](t, system, authority, conflicting); result.Err == nil {
				t.Fatal("retry changed frozen creation input")
			}
			if !failStart {
				if result := askLifecycle[agentactor.Closed](t, system, authority, agentregistry.CloseAgent{Creation: input.Creation}); result.Err == nil {
					t.Fatal("close ignored component failure")
				}
			}
			if got := <-events; got != "stop-second" {
				t.Fatal(got)
			}
			select {
			case extra := <-events:
				t.Fatal("close advanced after failure", extra)
			default:
			}
			if result := askLifecycle[sessionactor.View](t, system, sessionRoute(t, system, "conversation"), sessionactor.Read{CreationID: "first"}); result.Err != nil {
				t.Fatal("Session closed before components settled", result.Err)
			}
			if result := askLifecycle[agentactor.Closed](t, system, authority, agentregistry.CloseAgent{Creation: input.Creation}); result.Err != nil {
				t.Fatal(result.Err)
			}
			if got := <-events; got != "stop-second" {
				t.Fatal(got)
			}
			if got := <-events; got != "stop-first" {
				t.Fatal(got)
			}
			if result := askLifecycle[sessionregistry.Sessions](t, system, input.SessionRegistry, sessionregistry.ListSessions{}); len(result.Items) != 0 {
				t.Fatal("Session survived successful shutdown")
			}
		})
	}
}

func TestSessionRecoveryFailureCanCloseUnclaimedCreation(t *testing.T) {
	system, authority, input := creationFixture(t)
	input.Restore = true
	result := askLifecycle[agentregistry.Ready](t, system, authority, input)
	if result.Err == nil || result.Executor != nil {
		t.Fatal("missing Session recovery was published")
	}
	if result := askLifecycle[agentactor.Closed](t, system, authority, agentregistry.CloseAgent{Creation: input.Creation}); result.Err != nil {
		t.Fatal("unclaimed creation leaked its reservation", result.Err)
	}
	input.Creation.CreationID = "second"
	input.Restore = false
	replacement := askLifecycle[agentregistry.Ready](t, system, authority, input)
	if replacement.Err != nil {
		t.Fatal(replacement.Err)
	}
	if result := askLifecycle[agentactor.Closed](t, system, authority, agentregistry.CloseAgent{Creation: input.Creation}); result.Err != nil {
		t.Fatal(result.Err)
	}
}

func sessionRoute(t *testing.T, system goakt.ActorSystem, name string) *goakt.GrainIdentity {
	t.Helper()
	identity, err := goakt.GrainOf[*sessionactor.Grain](context.Background(), system, sessionactor.RuntimeName(name), goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func TestResolveRejectsUnexpectedExecutorReactivation(t *testing.T) {
	system, authority, input := creationFixture(t)
	current := askLifecycle[agentregistry.Ready](t, system, authority, input)
	if current.Err != nil {
		t.Fatal(current.Err)
	}
	if err := system.TellGrain(context.Background(), current.Executor, new(goakt.PoisonPill)); err != nil {
		t.Fatal(err)
	}
	if result := askLifecycle[agentregistry.Ready](t, system, authority, agentregistry.FindAgent{AgentID: input.Creation.AgentID}); result.Err == nil || result.Executor != nil {
		t.Fatal("unexpected activation was published")
	}
	replacement := input
	replacement.Creation.CreationID = "replacement"
	if result := askLifecycle[agentregistry.Ready](t, system, authority, replacement); result.Err == nil {
		t.Fatal("lost execution state released the creation grant")
	}
	if result := askLifecycle[agentactor.Closed](t, system, authority, agentregistry.CloseAgent{Creation: input.Creation}); result.Err == nil {
		t.Fatal("unconfirmed cleanup released ownership")
	}
}

func TestSessionIDIndexDoesNotParseActorNames(t *testing.T) {
	system, authority, input := creationFixture(t)
	input.Creation.AgentID = "tenant.session.agentloop"
	current := askLifecycle[agentregistry.Ready](t, system, authority, input)
	if current.Err != nil {
		t.Fatal(current.Err)
	}
	if current.Executor.Name() != "tenant.session.agentloop.agentloop" || current.Commands.Name() != "tenant.session.agentloop.commands" {
		t.Fatal("service address was not generated from the complete SessionID")
	}
	resolved := askLifecycle[agentregistry.Ready](t, system, authority, agentregistry.FindAgent{AgentID: input.Creation.AgentID})
	if resolved.Err != nil || resolved.Creation != input.Creation || !resolved.Executor.Equal(current.Executor) {
		t.Fatal("SessionID lookup lost its business identity", resolved.Err)
	}
	view := askLifecycle[sessionactor.View](t, system, sessionRoute(t, system, string(input.Creation.AgentID)), sessionactor.Read{CreationID: input.Creation.CreationID})
	if view.Err != nil || view.Header.ID != input.Creation.AgentID {
		t.Fatal("runtime name leaked into Session header", view.Err)
	}
	if result := askLifecycle[agentactor.Closed](t, system, authority, agentregistry.CloseAgent{Creation: input.Creation}); result.Err != nil {
		t.Fatal(result.Err)
	}
}

func TestCancelledInboxSurvivesRegistryCloseAndSQLiteRestore(t *testing.T) {
	system, authority, input := creationFixture(t)
	current := askLifecycle[agentregistry.Ready](t, system, authority, input)
	if current.Err != nil {
		t.Fatal(current.Err)
	}
	if result := askLifecycle[agentactor.MaintenanceAccepted](t, system, current.Executor, agentactor.BeginMaintenance{
		CreationID: input.Creation.CreationID,
		RequestID:  "maintenance",
		Worker:     input.ModelConfig,
	}); result.Err != nil {
		t.Fatal(result.Err)
	}
	var messages []agentmessage.UserMessage
	for _, text := range []string{"first retained input", "second retained input"} {
		message, err := agentmessage.NewUserMessage(agentmessage.UserMessageInput{
			Content: []agentmessage.ContentBlock{agentmessage.NewTextBlock(text)},
			Source:  agentmessage.UserMessageSource{},
		})
		if err != nil {
			t.Fatal(err)
		}
		messages = append(messages, message)
		if result := askLifecycle[loopactor.Changed](t, system, current.Executor, loopactor.Inject{
			CreationID: input.Creation.CreationID,
			Message:    message,
		}); result.Err != nil {
			t.Fatal(result.Err)
		}
	}
	if result := askLifecycle[loopactor.Changed](t, system, current.Executor, loopactor.Cancel{
		CreationID: input.Creation.CreationID,
		Cause:      agent.UserCancel{},
		Options:    agent.CancelOptions{KeepInbox: true},
	}); result.Err != nil {
		t.Fatal(result.Err)
	}
	closed := make(chan response, 1)
	go func() {
		payload, err := system.AskGrain(context.Background(), authority, agentregistry.CloseAgent{Creation: input.Creation}, 3*time.Second)
		closed <- response{
			value: payload,
			err:   err,
		}
	}()
	deadline := time.Now().Add(time.Second)
	for {
		probe := askLifecycle[agentactor.MaintenanceAccepted](t, system, current.Executor, agentactor.BeginMaintenance{
			CreationID: input.Creation.CreationID,
			RequestID:  "probe",
			Worker:     input.ModelConfig,
		})
		if errors.Is(probe.Err, agent.ErrClosing) {
			break
		}
		if !errors.Is(probe.Err, agent.ErrBusy) || time.Now().After(deadline) {
			t.Fatal("close did not freeze AgentLoop", probe.Err)
		}
	}
	select {
	case result := <-closed:
		t.Fatal("cancellation acknowledgement released active maintenance", result)
	default:
	}
	if result := askLifecycle[agentactor.MaintenanceFinished](t, system, current.Executor, agentactor.FinishMaintenance{
		CreationID: input.Creation.CreationID,
		RequestID:  "maintenance",
	}); result.Err != nil {
		t.Fatal(result.Err)
	}
	result := <-closed
	if result.err != nil {
		t.Fatal(result.err)
	}
	if failure := result.value.(agentactor.Closed).Err; failure != nil {
		t.Fatal(failure)
	}

	input.Creation.CreationID = "restored"
	input.Restore = true
	restored := askLifecycle[agentregistry.Ready](t, system, authority, input)
	if restored.Err != nil {
		t.Fatal(restored.Err)
	}
	activity := askLifecycle[loopactor.Activity](t, system, restored.Executor, loopactor.ReadActivity{CreationID: input.Creation.CreationID})
	if activity.Err != nil || len(activity.Pending.NextStep) != len(messages) {
		t.Fatal("restoration lost retained Inbox", activity.Err, activity.Pending)
	}
	for index, message := range messages {
		if activity.Pending.NextStep[index].StableID() != message.StableID() {
			t.Fatal("restoration changed identity or order")
		}
	}
	if result := askLifecycle[loopactor.Changed](t, system, restored.Executor, loopactor.Cancel{
		CreationID: input.Creation.CreationID,
		Cause:      agent.UserCancel{},
		Options:    agent.CancelOptions{KeepInbox: true},
	}); result.Err != nil {
		t.Fatal(result.Err)
	}
	if result := askLifecycle[agentactor.Closed](t, system, authority, agentregistry.CloseAgent{Creation: input.Creation}); result.Err != nil {
		t.Fatal(result.Err)
	}
}
