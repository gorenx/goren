package registry_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	agentactor "github.com/gorenx/goren/agent/actor"
	"github.com/gorenx/goren/agent/registry"
	"github.com/gorenx/goren/toolaskuser"
	toolregistry "github.com/gorenx/goren/tools/registry"
	catalogactor "github.com/gorenx/goren/tools/registry/actor"
	"github.com/gorenx/goren/tools/tool"
	toolactor "github.com/gorenx/goren/tools/tool/actor"
	"github.com/gorenx/goren/userquestions"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
	"github.com/tochemey/goakt/v4/reentrancy"
)

type toolQuestions struct{}

func (toolQuestions) Ask(context.Context, userquestions.Request) (userquestions.Answer, error) {
	return userquestions.Answer{}, nil
}

type toolProvider struct {
	opens        atomic.Int32
	closes       atomic.Int32
	closeFailure atomic.Bool
}

type toolResource struct {
	closes  *atomic.Int32
	failure *atomic.Bool
}

func (owned *toolResource) Close(context.Context) error {
	owned.closes.Add(1)
	if owned.failure.Load() {
		return errors.New("resource cleanup failed")
	}
	return nil
}

func (source *toolProvider) Open(context.Context) (tool.Implementation, error) {
	source.opens.Add(1)
	definition, err := toolaskuser.NewDefinition(toolQuestions{})
	return tool.Implementation{
		Definition: definition,
		Resource: &toolResource{
			closes:  &source.closes,
			failure: &source.closeFailure,
		},
	}, err
}

func startToolLifecycle(t *testing.T, expectCloseFailure bool) (goakt.ActorSystem, *goakt.GrainIdentity, agentactor.CreateTool) {
	t.Helper()
	system, err := goakt.NewActorSystem("tool-lifecycle", goakt.WithPubSub(), goakt.WithLogger(log.DiscardLogger))
	if err != nil {
		t.Fatal(err)
	}
	if err := system.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := system.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})
	authority, err := goakt.GrainOf[*registry.Grain](context.Background(), system, registry.Name,
		goakt.WithLongLivedGrain(),
		goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		t.Fatal(err)
	}
	directory, err := goakt.GrainOf[*catalogactor.RegistryGrain](context.Background(), system, catalogactor.RegistryName, goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	creation := registry.Creation{
		AgentID:    "agent",
		CreationID: "agent-creation",
	}
	if reply := askLifecycle[registry.Completion](t, system, authority, registry.ReserveAgent{
		Creation: creation,
	}); reply.Err != nil {
		t.Fatal(reply.Err)
	}
	t.Cleanup(func() {
		if reply := askLifecycle[agentactor.Closed](t, system, authority, registry.CloseAgent{
			Creation: creation,
		}); (reply.Err != nil) != expectCloseFailure {
			t.Errorf("Agent close error = %v, expected failure = %v", reply.Err, expectCloseFailure)
		}
	})
	return system, authority, agentactor.CreateTool{
		AgentID:         creation.AgentID,
		AgentCreationID: creation.CreationID,
		Name:            toolaskuser.Name,
		Registry:        directory,
		Timeout:         time.Second,
	}
}

func provisionOwnedTool(t *testing.T, system goakt.ActorSystem, input agentactor.CreateTool, instance agentactor.ToolInstance, source *toolProvider) {
	t.Helper()
	if reply := askLifecycle[catalogactor.Completion](t, system, input.Registry, catalogactor.InitializeDefaults{
		Tools: []toolregistry.Tool{{
			Name:     toolaskuser.Name,
			Provider: source,
			Visible:  true,
		}},
	}); reply.Err != nil {
		t.Fatal(reply.Err)
	}
	if reply := askLifecycle[catalogactor.Configuration](t, system, input.Registry, catalogactor.RegisterConfiguration{
		ID:      "initial",
		AgentID: input.AgentID,
	}); reply.Err != nil {
		t.Fatal(reply.Err)
	}
	revision := tool.Revision("initial")
	if reply := askLifecycle[toolactor.Completion](t, system, instance.Identity, toolactor.Provision{
		CreationID: instance.CreationID,
		Revision:   revision,
		Provider:   source,
		Visible:    true,
	}); reply.Err != nil {
		t.Fatal(reply.Err)
	}
	if reply := askLifecycle[catalogactor.Catalog](t, system, input.Registry, catalogactor.PublishDefinitions{
		ID: "initial",
	}); reply.Err != nil {
		t.Fatal(reply.Err)
	}
	if reply := askLifecycle[toolactor.Completion](t, system, instance.Identity, toolactor.Commit{
		CreationID: instance.CreationID,
		Revision:   revision,
	}); reply.Err != nil {
		t.Fatal(reply.Err)
	}
}

func TestToolLifecycleKeepsIdentityAndRejectsOldRemoval(t *testing.T) {
	system, authority, input := startToolLifecycle(t, false)
	first := askLifecycle[agentactor.ToolInstance](t, system, authority, input)
	if first.Err != nil || first.Identity == nil || first.CreationID == "" || first.CreationID == input.AgentCreationID {
		t.Fatalf("tool creation = %#v", first)
	}
	repeated := askLifecycle[agentactor.ToolInstance](t, system, authority, input)
	if repeated.Err != nil || repeated.CreationID != first.CreationID || repeated.Identity.String() != first.Identity.String() {
		t.Fatalf("duplicate creation replaced tool: %#v", repeated)
	}
	remove := agentactor.RemoveTool{
		AgentID:         input.AgentID,
		AgentCreationID: input.AgentCreationID,
		Name:            input.Name,
		ToolCreationID:  first.CreationID,
	}
	if reply := askLifecycle[agentactor.ToolRemoved](t, system, authority, remove); reply.Err != nil {
		t.Fatal(reply.Err)
	}
	if reply := askLifecycle[agentactor.ToolRemoved](t, system, authority, remove); reply.Err != nil {
		t.Fatal(reply.Err)
	}
	second := askLifecycle[agentactor.ToolInstance](t, system, authority, input)
	if second.Err != nil || second.CreationID == first.CreationID || second.Identity.String() != first.Identity.String() {
		t.Fatalf("recreated tool lifetime = %#v", second)
	}
	if reply := askLifecycle[agentactor.ToolRemoved](t, system, authority, remove); reply.Err == nil {
		t.Fatal("old removal deleted replacement")
	}
	if reply := askLifecycle[toolactor.Completion](t, system, second.Identity, toolactor.Close{
		CreationID: first.CreationID,
	}); reply.Err == nil {
		t.Fatal("old tool lifetime closed replacement resources")
	}
}

func TestAgentCloseRetainsToolCleanupFailureAndRetries(t *testing.T) {
	system, authority, input := startToolLifecycle(t, false)
	instance := askLifecycle[agentactor.ToolInstance](t, system, authority, input)
	if instance.Err != nil {
		t.Fatal(instance.Err)
	}
	source := &toolProvider{}
	t.Cleanup(func() { source.closeFailure.Store(false) })
	provisionOwnedTool(t, system, input, instance, source)
	source.closeFailure.Store(true)
	closeRequest := registry.CloseAgent{
		Creation: registry.Creation{
			AgentID:    input.AgentID,
			CreationID: input.AgentCreationID,
		},
	}
	if reply := askLifecycle[agentactor.Closed](t, system, authority, closeRequest); reply.Err == nil || source.closes.Load() != 1 {
		t.Fatalf("failed tool cleanup released Agent: %v, closes = %d", reply.Err, source.closes.Load())
	}
	if reply := askLifecycle[agentactor.ToolInstance](t, system, authority, input); reply.Err == nil {
		t.Fatal("closing Agent admitted tool creation")
	}
	source.closeFailure.Store(false)
	if reply := askLifecycle[agentactor.Closed](t, system, authority, closeRequest); reply.Err != nil || source.closes.Load() != 2 {
		t.Fatalf("tool cleanup retry = %v, closes = %d", reply.Err, source.closes.Load())
	}
	if reply := askLifecycle[agentactor.Closed](t, system, authority, closeRequest); reply.Err != nil || source.closes.Load() != 2 {
		t.Fatal("completed Agent close repeated resource release")
	}
}

// Forced deactivation loses the business owner; cleanup must report that it
// cannot confirm resource release, rather than authorize a replacement owner.
func TestStoppedToolActivationCannotReclaimExistingLifetime(t *testing.T) {
	system, authority, input := startToolLifecycle(t, true)
	instance := askLifecycle[agentactor.ToolInstance](t, system, authority, input)
	if instance.Err != nil {
		t.Fatal(instance.Err)
	}
	if err := system.TellGrain(context.Background(), instance.Identity, new(goakt.PoisonPill)); err != nil {
		t.Fatal(err)
	}
	if reply := askLifecycle[toolactor.Completion](t, system, instance.Identity, toolactor.Initialize{
		CreationID: instance.CreationID,
		AgentID:    input.AgentID,
		Name:       input.Name,
		Registry:   input.Registry,
		Authority:  authority,
	}); reply.Err == nil {
		t.Fatal("new activation claimed a prior tool lifetime")
	}
}

func TestToolRemovalFailureRejectsReuseUntilCleanupSucceeds(t *testing.T) {
	system, authority, input := startToolLifecycle(t, false)
	instance := askLifecycle[agentactor.ToolInstance](t, system, authority, input)
	if instance.Err != nil {
		t.Fatal(instance.Err)
	}
	source := &toolProvider{}
	t.Cleanup(func() { source.closeFailure.Store(false) })
	provisionOwnedTool(t, system, input, instance, source)
	source.closeFailure.Store(true)
	removal := agentactor.RemoveTool{
		AgentID:         input.AgentID,
		AgentCreationID: input.AgentCreationID,
		Name:            input.Name,
		ToolCreationID:  instance.CreationID,
	}
	if reply := askLifecycle[agentactor.ToolRemoved](t, system, authority, removal); reply.Err == nil {
		t.Fatal("removal ignored resource cleanup failure")
	}
	if reply := askLifecycle[agentactor.ToolInstance](t, system, authority, input); reply.Err == nil {
		t.Fatal("creation reused a tool whose removal failed")
	}
	source.closeFailure.Store(false)
	if reply := askLifecycle[agentactor.ToolRemoved](t, system, authority, removal); reply.Err != nil {
		t.Fatal(reply.Err)
	}
	replacement := askLifecycle[agentactor.ToolInstance](t, system, authority, input)
	if replacement.Err != nil || replacement.CreationID == instance.CreationID {
		t.Fatalf("replacement = %#v", replacement)
	}
	if source.opens.Load() != 1 || source.closes.Load() != 2 {
		t.Fatalf("opens = %d, closes = %d", source.opens.Load(), source.closes.Load())
	}
}
