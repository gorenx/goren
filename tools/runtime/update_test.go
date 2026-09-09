package toolruntime

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	toolregistry "github.com/gorenx/goren/tools/registry"
	"github.com/gorenx/goren/tools/tool"
)

type directory struct{ registry toolregistry.Registry }

func (source *directory) Read(_ context.Context, identifier session.SessionID) (toolregistry.Configuration, error) {
	return source.registry.Read(identifier), nil
}

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
	err = owner.Initialize(context.Background(), source, endpoint)
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
	complete, err := owner.Apply(context.Background(), source, endpoint)
	if complete || err == nil {
		t.Fatal("partial switch was reported complete")
	}
	_, err = owner.Snapshot()
	if err == nil {
		t.Fatal("partial switch exposed a snapshot")
	}
	endpoint.failure = ""
	complete, err = owner.Apply(context.Background(), source, endpoint)
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
	complete, err := owner.Apply(context.Background(), source, endpoint)
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
