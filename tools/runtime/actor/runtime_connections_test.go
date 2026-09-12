package runtimeactor_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/gorenx/goren/mcpclient"
	"github.com/gorenx/goren/tools"
	toolregistry "github.com/gorenx/goren/tools/registry"
	catalogactor "github.com/gorenx/goren/tools/registry/actor"
	runtimeactor "github.com/gorenx/goren/tools/runtime/actor"
)

func TestRuntimeConnectionRetryDoesNotAcquireTurnOrToolBoundary(t *testing.T) {
	firstRemote, firstConfig := serveMCP(t, "first")
	secondRemote, secondConfig := serveMCP(t, "second")
	system, registry, _ := startCatalog(t, true, nil)
	first, _ := newMCPRuntime(t, system, registry, "first", &toolregistry.Catalog{}, []mcpclient.Config{firstConfig})
	second, _ := newMCPRuntime(t, system, registry, "second", &toolregistry.Catalog{}, []mcpclient.Config{secondConfig})
	if err := startClient(t, system, first, t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := startClient(t, system, second, t.Context()); err != nil {
		t.Fatal(err)
	}
	name := mcpclient.ToolName("fixture", "read")
	original := ask[runtimeactor.RuntimeView](t, system, first.Runtime(), runtimeactor.ReadRuntime{
		CreationID: "creation",
	})
	address := ask[catalogactor.ToolAddress](t, system, registry, catalogactor.FindTool{
		AgentID: "first",
		Name:    name,
	})
	prepared := ask[runtimeactor.CallResult](t, system, first.Runtime(), runtimeactor.PrepareCall{
		CreationID: "creation",
		Context:    t.Context(),
		Input: tools.ToolExecutionInput{
			SessionID: "first",
			CallID:    "read-call",
			Name:      name,
			Arguments: json.RawMessage(`{}`),
		},
	})
	if prepared.Err != nil {
		t.Fatal(prepared.Err)
	}
	firstRemote.pauseDiscovery.Store(true)
	result := make(chan runtimeactor.Completion, 1)
	go func() {
		payload, err := system.AskGrain(t.Context(), first.Runtime(), runtimeactor.RetryConnection{
			CreationID: "creation",
			Name:       "fixture",
			Context:    t.Context(),
		}, 3*time.Second)
		if err != nil {
			result <- runtimeactor.Completion{
				Err: err,
			}
			return
		}
		result <- payload.(runtimeactor.Completion)
	}()
	select {
	case <-firstRemote.discovering:
	case <-time.After(3 * time.Second):
		t.Fatal("retry waited for the retained tool call")
	}
	listed := ask[runtimeactor.Connections](t, system, first.Runtime(), runtimeactor.ReadConnections{
		CreationID: "creation",
	})
	if listed.Err != nil || len(listed.Values) != 1 || listed.Values[0].Status != runtimeactor.ConnectionReconnecting {
		t.Fatalf("missing independent retry state: %+v", listed)
	}
	finishMCP(t, system, first.Runtime(), firstRemote.result)
	invokeMCP(t, system, second.Runtime(), "second", name, secondRemote.result)
	// A Turn starting during discovery may use the already applied definitions;
	// it must not fail or wait for transport recovery to settle.
	definitions := ask[runtimeactor.Definitions](t, system, first.Runtime(), runtimeactor.RefreshConfiguration{
		CreationID: "creation",
		ID:         "during-retry",
	})
	if definitions.Err != nil || len(definitions.Value.Schemas) != 1 {
		t.Fatalf("retry blocked ordinary Turn configuration: %+v", definitions)
	}
	close(firstRemote.resume)
	if completed := <-result; completed.Err != nil {
		t.Fatal(completed.Err)
	}
	// No RefreshConfiguration or ToolActor replacement occurs after recovery.
	invokeMCP(t, system, first.Runtime(), "first", name, firstRemote.result)
	after := ask[runtimeactor.RuntimeView](t, system, first.Runtime(), runtimeactor.ReadRuntime{
		CreationID: "creation",
	})
	current := ask[catalogactor.ToolAddress](t, system, registry, catalogactor.FindTool{
		AgentID: "first",
		Name:    name,
	})
	if original.Err != nil || after.Err != nil || !reflect.DeepEqual(original.Snapshot, after.Snapshot) ||
		!address.Found || !current.Found || address.CreationID != current.CreationID || !address.Actor.Equal(current.Actor) {
		t.Fatal("same-definition retry replaced the tool implementation or catalog")
	}
	if firstRemote.listens.Load() != 2 || secondRemote.listens.Load() != 1 || secondRemote.closed.Load() != 0 {
		t.Fatal("retry escaped the selected Session")
	}
	listed.Values[0].Name = "mutated"
	for _, creation := range []string{"old", ""} {
		stale := ask[runtimeactor.Connections](t, system, first.Runtime(), runtimeactor.ReadConnections{
			CreationID: creation,
		})
		if stale.Err == nil || len(stale.Values) != 0 {
			t.Fatal("stale creation read current connection state")
		}
	}
	listed = ask[runtimeactor.Connections](t, system, first.Runtime(), runtimeactor.ReadConnections{
		CreationID: "creation",
	})
	if listed.Err != nil || len(listed.Values) != 1 || listed.Values[0].Name != "fixture" || listed.Values[0].Status != runtimeactor.ConnectionConnected {
		t.Fatalf("query mutated internal state: %+v", listed)
	}
}

func TestRuntimeCloseCancelsConnectionRetry(t *testing.T) {
	remote, configuration := serveMCP(t, "fixture")
	system, registry, _ := startCatalog(t, true, nil)
	sender, authority := newMCPRuntime(t, system, registry, "agent", &toolregistry.Catalog{}, []mcpclient.Config{configuration})
	if err := startClient(t, system, sender, t.Context()); err != nil {
		t.Fatal(err)
	}
	remote.pauseDiscovery.Store(true)
	result := make(chan error, 1)
	go func() {
		payload, err := system.AskGrain(t.Context(), sender.Runtime(), runtimeactor.RetryConnection{
			CreationID: "creation",
			Name:       "fixture",
			Context:    t.Context(),
		}, 3*time.Second)
		if err == nil {
			err = payload.(runtimeactor.Completion).Err
		}
		result <- err
	}()
	select {
	case <-remote.discovering:
	case <-time.After(3 * time.Second):
		t.Fatal("retry did not start")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := closeClient(t, system, sender, ctx, ask[string](t, system, authority, readClaim{})); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err == nil {
		t.Fatal("closure did not cancel recovery")
	}
	awaitMCPClosed(t, remote)
}
