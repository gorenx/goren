package runtimeactor_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	agentactor "github.com/gorenx/goren/agent/actor"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/toolaskuser"
	toolregistry "github.com/gorenx/goren/tools/registry"
	catalogactor "github.com/gorenx/goren/tools/registry/actor"
	runtimeactor "github.com/gorenx/goren/tools/runtime/actor"
	"github.com/gorenx/goren/tools/tool"
	toolactor "github.com/gorenx/goren/tools/tool/actor"
	"github.com/gorenx/goren/userquestions"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

type questions struct{}

func (questions) Ask(context.Context, userquestions.Request) (userquestions.Answer, error) {
	return userquestions.Answer{
		Answers: []userquestions.AnswerItem{},
	}, nil
}

type source struct {
	opens        atomic.Int32
	closes       atomic.Int32
	failure      bool
	closeFailure atomic.Bool
}

type resource struct {
	closes  *atomic.Int32
	failure *atomic.Bool
}

func (owned *resource) Close(context.Context) error {
	owned.closes.Add(1)
	if owned.failure.Load() {
		return errors.New("tool cleanup failed")
	}
	return nil
}
func (provider *source) Open(context.Context) (tool.Implementation, error) {
	provider.opens.Add(1)
	if provider.failure {
		return tool.Implementation{}, errors.New("tool construction failed")
	}
	definition, err := toolaskuser.NewDefinition(questions{})
	return tool.Implementation{
		Definition: definition,
		Resource: &resource{
			closes:  &provider.closes,
			failure: &provider.closeFailure,
		},
	}, err
}

// runtimeAuthority represents only the upstream activation grant. Production
// creation and descendant cleanup are covered by internal/server integration.
type runtimeAuthority struct{ instanceID string }
type readClaim struct{}

func (*runtimeAuthority) OnActivate(context.Context, *goakt.GrainProps) error   { return nil }
func (*runtimeAuthority) OnDeactivate(context.Context, *goakt.GrainProps) error { return nil }
func (owner *runtimeAuthority) OnReceive(gc *goakt.GrainContext) {
	switch input := gc.Message().(type) {
	case agentactor.ClaimTools:
		var err error
		if owner.instanceID != "" && owner.instanceID != input.InstanceID {
			err = errors.New("activation conflict")
		} else {
			owner.instanceID = input.InstanceID
		}
		gc.Response(agentactor.Claimed{Err: err})
	case readClaim:
		gc.Response(owner.instanceID)
	}
}

func startRuntime(t *testing.T, provider *source) (goakt.ActorSystem, *goakt.GrainIdentity, *goakt.GrainIdentity, runtimeactor.CloseRuntime) {
	t.Helper()
	system, registry, _ := startCatalog(t, true, tool.Providers{toolaskuser.Name: provider})
	definition, err := toolaskuser.NewDefinition(questions{})
	if err != nil {
		t.Fatal(err)
	}
	configured := ask[catalogactor.Completion](t, system, registry, catalogactor.RegisterSource{
		Name: "local",
		Catalog: toolregistry.Catalog{Schemas: []llm.ToolSchema{{
			Name:        definition.Name,
			Description: definition.Description,
			Parameters:  definition.Parameters,
		}}},
	})
	if configured.Err != nil {
		t.Fatal(configured.Err)
	}
	target, closing := initializeRuntime(t, system, registry)
	return system, target, registry, closing
}

func initializeRuntime(t *testing.T, system goakt.ActorSystem, registry *goakt.GrainIdentity) (*goakt.GrainIdentity, runtimeactor.CloseRuntime) {
	t.Helper()
	authority, err := goakt.GrainOf[*runtimeAuthority](context.Background(), system, "authority", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	target, err := goakt.GrainOf[*runtimeactor.Runtime](context.Background(), system, "agent.tools", goakt.WithLongLivedGrain(), goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		t.Fatal(err)
	}
	initialized := ask[runtimeactor.Completion](t, system, target, runtimeactor.InitializeRuntime{
		AgentID:    "agent",
		CreationID: "creation",
		Authority:  authority,
		Registry:   registry,
		Timeout:    time.Second,
	})
	if initialized.Err != nil {
		t.Fatal(initialized.Err)
	}
	return target, runtimeactor.CloseRuntime{
		CreationID: "creation",
		InstanceID: ask[string](t, system, authority, readClaim{}),
	}
}

func TestRuntimeRetainsFlagsAndRetriesFailedClose(t *testing.T) {
	provider := &source{}
	system, target, registry, closing := startRuntime(t, provider)
	route := ask[catalogactor.ToolAddress](t, system, registry, catalogactor.FindTool{
		AgentID: "agent",
		Name:    toolaskuser.Name,
	})
	if !route.Found || provider.opens.Load() != 1 {
		t.Fatal("ToolActor was not initialized and registered")
	}
	hidden := false
	configured := ask[catalogactor.Completion](t, system, registry, catalogactor.ConfigureSession{
		SessionID: "agent",
		Catalog: toolregistry.Catalog{
			InheritGlobal: true,
			Policies: map[string]toolregistry.Policy{
				toolaskuser.Name: {
					Visible: &hidden,
				},
			},
		},
	})
	if configured.Err != nil {
		t.Fatal(configured.Err)
	}
	updated := ask[runtimeactor.Definitions](t, system, target, runtimeactor.RefreshConfiguration{
		CreationID: "creation",
		ID:         "hide",
	})
	if updated.Err != nil || len(updated.Value.Schemas) != 1 || len(updated.Value.KnownNames) != 1 {
		t.Fatalf("updated: %+v", updated)
	}
	if provider.opens.Load() != 1 || provider.closes.Load() != 0 {
		t.Fatal("registration changed a running implementation")
	}
	provider.closeFailure.Store(true)
	closed := ask[runtimeactor.Completion](t, system, target, closing)
	if closed.Err == nil {
		t.Fatal("failed resource close was discarded")
	}
	route = ask[catalogactor.ToolAddress](t, system, registry, catalogactor.FindTool{
		AgentID: "agent",
		Name:    toolaskuser.Name,
	})
	if !route.Found {
		t.Fatal("failed close withdrew the remaining resource")
	}
	provider.closeFailure.Store(false)
	closed = ask[runtimeactor.Completion](t, system, target, closing)
	if closed.Err != nil {
		t.Fatal(closed.Err)
	}
	route = ask[catalogactor.ToolAddress](t, system, registry, catalogactor.FindTool{
		AgentID: "agent",
		Name:    toolaskuser.Name,
	})
	if route.Found || provider.opens.Load() != 1 || provider.closes.Load() != 2 {
		t.Fatal("cleanup reopened work or retained its destination")
	}
}

func TestRuntimeRejectsToolReactivationWithPreviousCreation(t *testing.T) {
	provider := &source{}
	system, target, registry, closing := startRuntime(t, provider)
	route := ask[catalogactor.ToolAddress](t, system, registry, catalogactor.FindTool{
		AgentID: "agent",
		Name:    toolaskuser.Name,
	})
	err := system.TellGrain(context.Background(), route.Actor, new(goakt.PoisonPill))
	if err != nil {
		t.Fatal(err)
	}
	initialized := ask[toolactor.Completion](t, system, route.Actor, toolactor.Initialize{
		AgentCreationID: closing.CreationID,
		CreationID:      route.CreationID,
		AgentID:         "agent",
		Name:            toolaskuser.Name,
		Authority:       target,
	})
	if initialized.Err == nil {
		t.Fatal("fresh activation claimed the old resource lifetime")
	}
	closed := ask[runtimeactor.Completion](t, system, target, closing)
	if closed.Err == nil || provider.closes.Load() != 0 {
		t.Fatal("new activation falsely confirmed release of old work")
	}
}
