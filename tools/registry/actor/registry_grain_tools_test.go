package toolactor_test

import (
	"context"
	"testing"

	toolactor "github.com/gorenx/goren/tools/registry/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
)

func TestToolLookupUsesNameAndAgentOverride(t *testing.T) {
	system, directory := startRegistry(t, false)
	global, err := goakt.GrainOf[*toolactor.RegistryGrain](context.Background(), system, "global-tool", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	local, err := goakt.GrainOf[*toolactor.RegistryGrain](context.Background(), system, "agent-tool", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	registration := toolactor.RegisterTool{
		Name:       "approval",
		CreationID: "global-1",
		Actor:      global,
	}
	for range 2 {
		reply := ask[toolactor.Completion](t, system, directory, registration)
		if reply.Err != nil {
			t.Fatal(reply.Err)
		}
	}
	inherited := ask[toolactor.ToolAddress](t, system, directory, toolactor.FindTool{
		AgentID: "session-1",
		Name:    "approval",
	})
	if !inherited.Found || inherited.Actor.String() != global.String() {
		t.Fatal("global tool was not inherited")
	}
	registration.AgentID = "session-1"
	registration.Actor = local
	registration.CreationID = "local-1"
	reply := ask[toolactor.Completion](t, system, directory, registration)
	if reply.Err != nil {
		t.Fatal(reply.Err)
	}
	selected := ask[toolactor.ToolAddress](t, system, directory, toolactor.FindTool{
		AgentID: "session-1",
		Name:    "approval",
	})
	if !selected.Found || selected.Actor.String() != local.String() {
		t.Fatal("Agent override was ignored")
	}
	reply = ask[toolactor.Completion](t, system, directory, toolactor.UnregisterTool{
		AgentID:    "session-1",
		Name:       "approval",
		CreationID: "old-local",
	})
	if reply.Err != nil {
		t.Fatal(reply.Err)
	}
	selected = ask[toolactor.ToolAddress](t, system, directory, toolactor.FindTool{
		AgentID: "session-1",
		Name:    "approval",
	})
	if selected.CreationID != "local-1" {
		t.Fatal("old withdrawal removed current Actor")
	}
	reply = ask[toolactor.Completion](t, system, directory, toolactor.UnregisterTool{
		AgentID:    "session-1",
		Name:       "approval",
		CreationID: "local-1",
	})
	if reply.Err != nil {
		t.Fatal(reply.Err)
	}
	selected = ask[toolactor.ToolAddress](t, system, directory, toolactor.FindTool{
		AgentID: "session-1",
		Name:    "approval",
	})
	if selected.CreationID != "global-1" {
		t.Fatal("withdrawal did not restore global fallback")
	}
}
