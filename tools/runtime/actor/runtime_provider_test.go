package runtimeactor_test

import (
	"encoding/json"
	"github.com/gorenx/goren/agentmessage"
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

func TestRuntimeRetainsProviderUntilClosed(t *testing.T) {
	first := &identifiedSource{
		id: "first",
	}
	second := &identifiedSource{
		id: "second",
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
	run := func(identifier agentmessage.CallID) {
		t.Helper()
		prepared := ask[runtimeactor.CallResult](t, system, target, runtimeactor.PrepareCall{
			CreationID: "creation",
			Context:    t.Context(),
			Input: tools.ToolExecutionInput{
				SessionID: "agent",
				CallID:    identifier,
				Name:      toolaskuser.Name,
				Arguments: json.RawMessage(`{"questions":[{"id":"q","question":"Continue?"}]}`),
			},
		})
		if prepared.Err != nil || prepared.Stage != tools.ScheduledDispatch {
			t.Fatalf("prepare: %+v", prepared)
		}
		dispatched := ask[runtimeactor.CallResult](t, system, target, runtimeactor.DispatchCall{
			CreationID: "creation",
			CallID:     identifier,
		})
		if dispatched.Err != nil {
			t.Fatal(dispatched.Err)
		}
		finalized := ask[runtimeactor.CallResult](t, system, target, runtimeactor.FinalizeCall{
			CreationID: "creation",
			CallID:     identifier,
		})
		if finalized.Err != nil || finalized.Result == nil || finalized.Result.Failed() {
			t.Fatalf("finalize: %+v", finalized)
		}
		released := ask[runtimeactor.Completion](t, system, target, runtimeactor.ReleaseCall{
			CreationID: "creation",
			CallID:     identifier,
		})
		if released.Err != nil {
			t.Fatal(released.Err)
		}
	}
	run("first-call")
	run("repeated-call")
	refresh := func(identifier string) runtimeactor.Definitions {
		return ask[runtimeactor.Definitions](t, system, target, runtimeactor.RefreshConfiguration{
			CreationID: "creation",
			ID:         identifier,
		})
	}
	dynamic.current.Store(second)
	publish(second.id)
	after := refresh("registered-replacement")
	if after.Err != nil || first.closes.Load() != 0 || second.opens.Load() != 0 {
		t.Fatal("registration mutated a running lifetime")
	}
	run("original-binding")
	current := ask[catalogactor.ToolAddress](t, system, registry, catalogactor.FindTool{
		AgentID: "agent",
		Name:    definition.Name,
	})
	if current.CreationID != address.CreationID || current.Actor.String() != address.Actor.String() {
		t.Fatal("registration replaced ToolActor")
	}
	closed := ask[runtimeactor.Completion](t, system, target, closing)
	if closed.Err != nil || first.closes.Load() != 1 {
		t.Fatalf("close: %+v", closed)
	}

}

// Wait for asynchronous MCP discovery without assuming a remote notification
// has already been processed.
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
