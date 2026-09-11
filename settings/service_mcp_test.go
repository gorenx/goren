package settings

import (
	"errors"
	"reflect"
	"testing"

	"github.com/gorenx/goren/mcpclient"
)

func namedMCP(name string, endpoint string) MCPConfig {
	return MCPConfig{
		Enabled: true,
		Connection: mcpclient.Config{
			ServerName: name,
			Transport:  "streamable-http",
			URL:        endpoint,
		},
	}
}

func TestServiceMCPReplacesNamedGroupsAndRestoresInheritance(t *testing.T) {
	baseline := exampleBaseline()
	first := namedMCP("testa", "https://local.example.test")
	first.Connection.MaxTools = 25
	first.Connection.SkipDiscover = true
	baseline.MCP["testa"] = first
	baseline.MCP["other"] = namedMCP("other", "https://other.example.test")
	storage := &memoryStore{}
	connections := &connectionRecorder{}
	owner, err := NewService(baseline, State{}, storage, &promptRecorder{}, connections, &toolRecorder{})
	if err != nil {
		t.Fatal(err)
	}

	replacement := namedMCP("testa", "https://user.example.test")
	view, err := owner.UpdateMCP(t.Context(), "testa", 0, replacement)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(view.Target.MCP["testa"], replacement) ||
		!reflect.DeepEqual(view.Target.MCP["other"], baseline.MCP["other"]) ||
		view.State.Version(mcpGroup("other")) != 0 {
		t.Fatal("replacement inherited old fields or changed another group")
	}
	if len(connections.definitions) != 2 || connections.definitions[0].ServerName != "other" {
		t.Fatal("application did not receive the complete sorted target")
	}

	other := namedMCP("other", "https://changed.example.test")
	view, err = owner.UpdateMCP(t.Context(), "other", 0, other)
	if err != nil || view.State.Version(mcpGroup("testa")) != 1 {
		t.Fatalf("independent group edit: %v", err)
	}
	// Revision 1 remains valid for testa despite the independent write.
	replacement.Enabled = false
	view, err = owner.UpdateMCP(t.Context(), "testa", 1, replacement)
	if err != nil ||
		len(connections.definitions) != 1 ||
		view.Target.MCP["testa"].Enabled {
		t.Fatalf("disable named connection: %v", err)
	}
	if _, err := owner.UpdateMCP(t.Context(), "testa", 1, replacement); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale edit accepted: %v", err)
	}
	view, err = owner.ResetMCP(t.Context(), "testa", view.State.Version(mcpGroup("testa")))
	if err != nil || !reflect.DeepEqual(view.Target.MCP["testa"], first) {
		t.Fatalf("reset did not restore the full local group: %v", err)
	}
	if _, present := view.State.User.MCP["testa"]; present {
		t.Fatal("reset retained an explicit override")
	}
	if view.State.Version(mcpGroup("testa")) != 4 || view.State.Version(mcpGroup("other")) != 2 {
		t.Fatal("reset lost its revision or changed another group's version")
	}
	if _, err := owner.UpdateMCP(t.Context(), "testa", 0, replacement); !errors.Is(err, ErrConflict) {
		t.Fatalf("reset permitted an obsolete creation request: %v", err)
	}
	view.State.Versions[mcpGroup("testa")] = 999
	delete(view.Target.MCP, "other")
	if owner.Snapshot().State.Version(mcpGroup("testa")) != 4 || len(owner.Snapshot().Target.MCP) != 2 {
		t.Fatal("snapshot leaked mutable maps")
	}
}

func TestServiceMCPRejectsIncompleteReplacementWithoutSaving(t *testing.T) {
	storage := &memoryStore{}
	owner, err := NewService(exampleBaseline(), State{}, storage, &promptRecorder{}, &connectionRecorder{}, &toolRecorder{})
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []MCPConfig{
		{},
		namedMCP("other", "https://example.test"),
		namedMCP("testa", ""),
	} {
		if _, err := owner.UpdateMCP(t.Context(), "testa", 0, candidate); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid replacement accepted: %v", err)
		}
	}
	if storage.stored.Revision != 0 {
		t.Fatal("invalid replacement changed persistent state")
	}
	if _, err := owner.ResetMCP(t.Context(), "unknown", 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("reset accepted an unknown group: %v", err)
	}
}
