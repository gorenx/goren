package toolruntime

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/gorenx/goren/llm"
	toolregistry "github.com/gorenx/goren/tools/registry"
	"github.com/gorenx/goren/tools/tool"
)

type directory struct{ registry toolregistry.Registry }

type work struct {
	// events records completed/attempted work operations in their observed order.
	events  []string
	failure string
}

func (endpoint *work) perform(name string) error {
	endpoint.events = append(endpoint.events, name)
	if name == endpoint.failure {
		return errors.New("work failed")
	}
	return nil
}
func (endpoint *work) Prepare(_ context.Context, _ tool.Revision, entry Tool) error {
	return endpoint.perform("prepare:" + entry.Schema.Name)
}
func (endpoint *work) Commit(_ context.Context, _ tool.Revision, name string) error {
	return endpoint.perform("commit:" + name)
}
func (endpoint *work) Retire(_ context.Context, _ tool.Revision, name string) error {
	return endpoint.perform("retire:" + name)
}
func (endpoint *work) Abort(_ context.Context, _ tool.Revision, name string) error {
	return endpoint.perform("abort:" + name)
}
func (endpoint *work) Remove(_ context.Context, name string) error {
	return endpoint.perform("remove:" + name)
}

func initializedRuntime(t *testing.T) (*Runtime, *directory, *work) {
	t.Helper()
	source := &directory{}
	err := source.registry.RegisterSource("local", toolregistry.Catalog{
		Schemas: []llm.ToolSchema{testSchema("read"), testSchema("removed")},
	})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := NewRuntime("agent")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := &work{}
	err = owner.Initialize(context.Background(), source.registry.Read("agent"), endpoint)
	if err != nil {
		t.Fatal(err)
	}
	endpoint.events = nil
	return owner, source, endpoint
}

func TestRuntimeSwitchesCompleteWorkBeforeExposingDefinitions(t *testing.T) {
	owner, source, endpoint := initializedRuntime(t)
	err := source.registry.RegisterSource("local", toolregistry.Catalog{
		Schemas: []llm.ToolSchema{testSchema("read"), testSchema("added")},
	})
	if err != nil {
		t.Fatal(err)
	}
	endpoint.failure = "commit:read"
	complete, err := owner.Apply(context.Background(), source.registry.Read("agent"), endpoint)
	if complete || err == nil {
		t.Fatal("partial switch was reported complete")
	}
	_, err = owner.Snapshot()
	if err == nil {
		t.Fatal("partial switch exposed a snapshot")
	}
	endpoint.failure = ""
	complete, err = owner.Apply(context.Background(), source.registry.Read("agent"), endpoint)
	if !complete || err != nil {
		t.Fatalf("retry=%v error=%v", complete, err)
	}
	expected := []string{"prepare:added", "prepare:read", "commit:added", "commit:read", "commit:read", "retire:added", "retire:read", "remove:removed"}
	if !slices.Equal(endpoint.events, expected) {
		t.Fatalf("operations=%v", endpoint.events)
	}
	snapshot, err := owner.Snapshot()
	if err != nil || !slices.Equal(snapshot.Names, []string{"added", "read"}) {
		t.Fatalf("snapshot=%+v error=%v", snapshot, err)
	}
}

func TestRuntimeRollsBackEveryAttemptAndPreservesOriginalWork(t *testing.T) {
	owner, source, endpoint := initializedRuntime(t)
	err := source.registry.RegisterSource("local", toolregistry.Catalog{
		Schemas: []llm.ToolSchema{testSchema("added"), testSchema("read")},
	})
	if err != nil {
		t.Fatal(err)
	}
	endpoint.failure = "prepare:read"
	complete, err := owner.Apply(context.Background(), source.registry.Read("agent"), endpoint)
	if !complete || err == nil {
		t.Fatal("failed preparation did not finish rollback")
	}
	expected := []string{"prepare:added", "prepare:read", "abort:added", "abort:read", "remove:added"}
	if !slices.Equal(endpoint.events, expected) {
		t.Fatalf("rollback=%v", endpoint.events)
	}
	snapshot, err := owner.Snapshot()
	if err != nil || !slices.Equal(snapshot.Names, []string{"read", "removed"}) {
		t.Fatal("rollback replaced original membership")
	}
	if !slices.ContainsFunc(snapshot.Configuration.Global.Schemas, func(entry llm.ToolSchema) bool {
		return entry.Name == "removed"
	}) {
		t.Fatal("rollback replaced the last applied configuration")
	}
}

func TestRuntimeSnapshotRetainsAppliedDisabledTools(t *testing.T) {
	owner, source, endpoint := initializedRuntime(t)
	accepted := toolregistry.Catalog{
		Schemas: []llm.ToolSchema{
			testSchema("read"),
			testSchema("removed"),
			testSchema("disabled"),
		},
		Policies: map[string]toolregistry.Policy{
			"disabled": {
				Enabled: new(false),
			},
		},
	}
	if err := source.registry.RegisterSource("local", accepted); err != nil {
		t.Fatal(err)
	}
	if complete, err := owner.Apply(t.Context(), source.registry.Read("agent"), endpoint); !complete || err != nil {
		t.Fatalf("apply: complete=%v error=%v", complete, err)
	}
	accepted.Schemas = append(accepted.Schemas, testSchema("pending"))
	if err := source.registry.RegisterSource("local", accepted); err != nil {
		t.Fatal(err)
	}
	snapshot, err := owner.Snapshot()
	if err != nil || !slices.Equal(snapshot.Names, []string{"read", "removed"}) {
		t.Fatalf("effective membership: %+v error=%v", snapshot, err)
	}
	if len(snapshot.Configuration.Global.Schemas) != 3 || snapshot.Configuration.Global.Policies["disabled"].Enabled == nil || *snapshot.Configuration.Global.Policies["disabled"].Enabled {
		t.Fatalf("applied disabled tool was lost or pending input leaked: %+v", snapshot.Configuration)
	}
	*snapshot.Configuration.Global.Policies["disabled"].Enabled = true
	again, err := owner.Snapshot()
	if err != nil || *again.Configuration.Global.Policies["disabled"].Enabled {
		t.Fatal("caller mutated the Runtime's applied configuration")
	}
}

func TestRuntimeRecoveryAppliesCurrentConfiguration(t *testing.T) {
	changed := testSchema("read")
	changed.Description = "Current definition"
	for _, scenario := range []struct {
		name    string
		schemas []llm.ToolSchema
		names   []string
	}{
		{
			name:    "addition",
			schemas: []llm.ToolSchema{testSchema("added"), testSchema("latest"), testSchema("read")},
			names:   []string{"added", "latest", "read"},
		},
		{
			name:    "definition-change",
			schemas: []llm.ToolSchema{changed},
			names:   []string{"read"},
		},
		{
			name: "removal",
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			owner, source, endpoint := initializedRuntime(t)
			if err := source.registry.RegisterSource("local", toolregistry.Catalog{
				Schemas: []llm.ToolSchema{testSchema("added"), testSchema("read")},
			}); err != nil {
				t.Fatal(err)
			}
			endpoint.failure = "retire:read"
			if complete, err := owner.Apply(t.Context(), source.registry.Read("agent"), endpoint); complete || err == nil {
				t.Fatal("retirement failure was reported complete")
			}
			if err := source.registry.RegisterSource("local", toolregistry.Catalog{
				Schemas: scenario.schemas,
			}); err != nil {
				t.Fatal(err)
			}
			endpoint.failure = ""
			endpoint.events = nil
			if complete, err := owner.Apply(t.Context(), source.registry.Read("agent"), endpoint); !complete || err != nil {
				t.Fatalf("recovery: complete=%v error=%v", complete, err)
			}
			captured, err := owner.Definitions()
			if err != nil || !slices.Equal(captured.KnownNames, scenario.names) {
				t.Fatalf("current target was not applied: %+v error=%v", captured, err)
			}
			if len(endpoint.events) < 2 || !slices.Equal(endpoint.events[:2], []string{"retire:read", "remove:removed"}) {
				t.Fatalf("recovery repeated confirmed work or started the new target too soon: %v", endpoint.events)
			}
			for index, schema := range captured.Schemas {
				if schema.Description != scenario.schemas[index].Description {
					t.Fatalf("stale definition: %+v", schema)
				}
			}
		})
	}
}

func TestRuntimeCloseRetainsFailedResourceAndRejectsReuse(t *testing.T) {
	owner, _, endpoint := initializedRuntime(t)
	endpoint.failure = "remove:removed"
	err := owner.Close(context.Background(), endpoint)
	if err == nil {
		t.Fatal("cleanup failure was discarded")
	}
	_, err = owner.Snapshot()
	if err == nil {
		t.Fatal("closing runtime admitted reads")
	}
	endpoint.failure = ""
	err = owner.Close(context.Background(), endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(endpoint.events, []string{"remove:read", "remove:removed", "remove:removed"}) {
		t.Fatalf("cleanup=%v", endpoint.events)
	}
}
