package runtimeactor_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/toolaskuser"
	"github.com/gorenx/goren/tools"
	toolregistry "github.com/gorenx/goren/tools/registry"
	catalogactor "github.com/gorenx/goren/tools/registry/actor"
	runtimeactor "github.com/gorenx/goren/tools/runtime/actor"
	"github.com/gorenx/goren/tools/tool"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

type identifiedSource struct {
	source
	id tools.ProviderID
}

func (provider *identifiedSource) ProviderID() tools.ProviderID { return provider.id }

type dynamicSource struct {
	current atomic.Pointer[identifiedSource]
}

func (provider *dynamicSource) Provider(name string, expected tools.ProviderID) (tool.Provider, bool) {
	selected := provider.current.Load()
	if name != toolaskuser.Name || selected == nil || selected.id != expected {
		return nil, false
	}
	return selected, true
}

func TestRuntimeRebindsProviderWithoutChangingSchemaOrActorIdentity(t *testing.T) {
	first := &identifiedSource{
		id: "first",
	}
	second := &identifiedSource{
		id: "second",
	}
	third := &identifiedSource{
		id: "third",
	}
	dynamic := &dynamicSource{}
	dynamic.current.Store(first)
	system, registry, _ := startCatalog(t, true, nil, dynamic)
	definition, err := toolaskuser.NewDefinition(questions{})
	if err != nil {
		t.Fatal(err)
	}
	publish := func(identifier tools.ProviderID) {
		t.Helper()
		configured := ask[catalogactor.Completion](t, system, registry, catalogactor.RegisterSource{
			Name: "remote",
			Catalog: toolregistry.Catalog{
				Schemas: []llm.ToolSchema{
					{
						Name:        definition.Name,
						Description: definition.Description,
						Parameters:  definition.Parameters,
					},
				},
				ProviderIDs: map[string]tools.ProviderID{
					definition.Name: identifier,
				},
			},
		})
		if configured.Err != nil {
			t.Fatal(configured.Err)
		}
	}
	publish(first.id)
	target, closing := initializeRuntime(t, system, registry)
	_, err = system.Spawn(context.Background(), "provider-watcher", &runtimeactor.Watcher{
		AgentID:    "agent",
		CreationID: "creation",
		Target:     target,
	}, goakt.WithReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		t.Fatal(err)
	}
	address := ask[catalogactor.ToolAddress](t, system, registry, catalogactor.FindTool{
		AgentID: "agent",
		Name:    definition.Name,
	})
	before := ask[runtimeactor.Definitions](t, system, target, runtimeactor.ReadDefinitions{
		CreationID: "creation",
	})
	if before.Err != nil ||
		!address.Found ||
		first.opens.Load() != 1 {
		t.Fatalf("initial binding: definitions=%+v address=%+v opens=%d", before, address, first.opens.Load())
	}
	refresh := func(identifier string) runtimeactor.Definitions {
		return ask[runtimeactor.Definitions](t, system, target, runtimeactor.RefreshConfiguration{
			CreationID: "creation",
			ID:         identifier,
		})
	}
	dynamic.current.Store(second)
	publish(second.id)
	after := awaitProviderRefresh(t, func() runtimeactor.Definitions {
		return refresh("replace")
	}, func(result runtimeactor.Definitions) bool {
		return result.Err != nil || second.opens.Load() != 0
	})
	if after.Err != nil ||
		first.opens.Load() != 1 ||
		first.closes.Load() != 1 ||
		second.opens.Load() != 1 {
		t.Fatalf("replacement=%+v first opens/closes=%d/%d second opens=%d", after, first.opens.Load(), first.closes.Load(), second.opens.Load())
	}
	if before.Value.Schemas[0].Version != after.Value.Schemas[0].Version {
		t.Fatal("Provider replacement changed model schema version")
	}
	current := ask[catalogactor.ToolAddress](t, system, registry, catalogactor.FindTool{
		AgentID: "agent",
		Name:    definition.Name,
	})
	if current.CreationID != address.CreationID || current.Actor.String() != address.Actor.String() {
		t.Fatal("Provider replacement recreated ToolActor")
	}
	if unchanged := refresh("unchanged"); unchanged.Err != nil || second.opens.Load() != 1 {
		t.Fatalf("unchanged Provider was reopened: %+v", unchanged)
	}
	// The catalog is published before its matching constructor becomes visible.
	// A refresh must reject the mismatch without opening the previous constructor.
	publish(third.id)
	failed := awaitProviderRefresh(t, func() runtimeactor.Definitions {
		return refresh("mismatch")
	}, func(result runtimeactor.Definitions) bool {
		return result.Err != nil
	})
	if failed.Err == nil ||
		second.opens.Load() != 1 ||
		second.closes.Load() != 0 {
		t.Fatalf("stale constructor: error=%v opens=%d closes=%d", failed.Err, second.opens.Load(), second.closes.Load())
	}
	dynamic.current.Store(third)
	recovered := refresh("recover")
	if recovered.Err != nil ||
		third.opens.Load() != 1 ||
		second.closes.Load() != 1 {
		t.Fatalf("binding recovery=%+v opens=%d old closes=%d", recovered, third.opens.Load(), second.closes.Load())
	}
	closed := ask[runtimeactor.Completion](t, system, target, closing)
	if closed.Err != nil || third.closes.Load() != 1 {
		t.Fatalf("close=%+v count=%d", closed, third.closes.Load())
	}
}

// Registry publications reach the Runtime through an asynchronous scope notice.
// Wait for observable application, without assuming publication is a mailbox barrier.
func awaitProviderRefresh(t *testing.T, refresh func() runtimeactor.Definitions, ready func(runtimeactor.Definitions) bool) runtimeactor.Definitions {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		result := refresh()
		if ready(result) {
			return result
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("Registry publication did not reach Runtime")
		}
	}
}
