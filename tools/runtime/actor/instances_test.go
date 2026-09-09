package runtimeactor_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	agentactor "github.com/gorenx/goren/agent/actor"
	agentregistry "github.com/gorenx/goren/agent/registry"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/toolaskuser"
	toolregistry "github.com/gorenx/goren/tools/registry"
	catalogactor "github.com/gorenx/goren/tools/registry/actor"
	toolruntime "github.com/gorenx/goren/tools/runtime"
	runtimeactor "github.com/gorenx/goren/tools/runtime/actor"
	"github.com/gorenx/goren/tools/tool"
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

func TestRuntimeAppliesRegistrySchemasThroughLifecycleAndCommonToolActor(t *testing.T) {
	system, registry, directory := startCatalog(t, true)
	ctx := context.Background()
	authority, err := goakt.GrainOf[*agentregistry.Grain](ctx, system, agentregistry.Name,
		goakt.WithLongLivedGrain(), goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		t.Fatal(err)
	}
	creation := agentregistry.Creation{
		AgentID:    "agent",
		CreationID: "creation",
	}
	reserved := ask[agentregistry.Completion](t, system, authority, agentregistry.ReserveAgent{Creation: creation})
	if reserved.Err != nil {
		t.Fatal(reserved.Err)
	}
	t.Cleanup(func() {
		reply := ask[agentactor.Closed](t, system, authority, agentregistry.CloseAgent{Creation: creation})
		if reply.Err != nil {
			t.Error(reply.Err)
		}
	})
	provider := &source{}
	t.Cleanup(func() { provider.closeFailure.Store(false) })
	instances, err := runtimeactor.NewInstances(system, authority, registry, creation.AgentID, creation.CreationID, time.Second,
		tool.Workers{toolaskuser.Name: provider})
	if err != nil {
		t.Fatal(err)
	}
	definition, err := toolaskuser.NewDefinition(questions{})
	if err != nil {
		t.Fatal(err)
	}
	input := toolregistry.Catalog{
		Schemas: []llm.ToolSchema{
			{
				Name:        definition.Name,
				Description: definition.Description,
				Parameters:  definition.Parameters,
			},
		},
	}
	reply := ask[catalogactor.Completion](t, system, registry, catalogactor.RegisterSource{
		Name:    "local",
		Catalog: input,
	})
	if reply.Err != nil {
		t.Fatal(reply.Err)
	}
	business, err := toolruntime.NewRuntime(creation.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	err = business.Initialize(ctx, directory, instances)
	if err != nil {
		t.Fatal(err)
	}
	route := ask[catalogactor.ToolAddress](t, system, registry, catalogactor.FindTool{
		AgentID: creation.AgentID,
		Name:    toolaskuser.Name,
	})
	if !route.Found || provider.opens.Load() != 1 {
		t.Fatal("work was not initialized and registered")
	}
	hidden := false
	err = directory.ConfigureSession(ctx, creation.AgentID, toolregistry.Catalog{
		Policies: map[string]toolregistry.Policy{
			toolaskuser.Name: {Visible: &hidden},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	complete, err := business.Apply(ctx, directory, instances)
	if !complete || err != nil {
		t.Fatalf("update=%v error=%v", complete, err)
	}
	definitions, err := business.Definitions()
	if err != nil || len(definitions.Schemas) != 0 || len(definitions.KnownNames) != 1 {
		t.Fatalf("hidden definitions=%+v error=%v", definitions, err)
	}
	if provider.opens.Load() != 2 || provider.closes.Load() != 1 {
		t.Fatal("replacement did not retire old work")
	}
	provider.closeFailure.Store(true)
	err = business.Close(ctx, instances)
	if err == nil {
		t.Fatal("failed resource close was discarded")
	}
	route = ask[catalogactor.ToolAddress](t, system, registry, catalogactor.FindTool{
		AgentID: creation.AgentID,
		Name:    toolaskuser.Name,
	})
	if !route.Found {
		t.Fatal("failed close withdrew the remaining resource")
	}
	provider.closeFailure.Store(false)
	err = business.Close(ctx, instances)
	if err != nil {
		t.Fatal(err)
	}
	route = ask[catalogactor.ToolAddress](t, system, registry, catalogactor.FindTool{
		AgentID: creation.AgentID,
		Name:    toolaskuser.Name,
	})
	if route.Found || provider.opens.Load() != 2 || provider.closes.Load() != 3 {
		t.Fatal("cleanup reopened work or retained a closed destination")
	}
}
