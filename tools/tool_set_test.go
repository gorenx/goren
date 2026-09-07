package tools_test

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/tools"
)

func toolSetDefinitions(names ...string) []tools.ToolDefinition {
	definitions := make([]tools.ToolDefinition, 0, len(names))
	for _, name := range names {
		definitions = append(
			definitions,
			objectTool(
				name,
				name+" description",
				tools.ExecutorFunc(passThroughBody),
			),
		)
	}
	return definitions
}

func schemaNames(runtime tools.ToolRuntime) []string {
	schemas := runtime.Schemas()
	names := make([]string, 0, len(schemas))
	for _, schema := range schemas {
		names = append(names, schema.Name)
	}
	return names
}

func TestToolSetOpenReplaceAndClosePublishOnlySnapshotChanges(t *testing.T) {
	var changes atomic.Int32
	observer := &eventObserverPlugin{
		name: "count-tool-set-changes",
		subscriptions: []plugin.EventSubscription{
			plugin.EventOf[tools.RegistryChanged](),
		},
		observe: func(context.Context, plugin.Event) error {
			changes.Add(1)
			return nil
		},
	}
	state := newToolsFixture(t, observer)
	requestContext := context.Background()
	toolSet, err := state.service.OpenToolSet(requestContext, "remote:one")
	if err != nil {
		t.Fatal(err)
	}
	if changes.Load() != 0 {
		t.Fatalf("OpenToolSet published %d changes", changes.Load())
	}
	if _, err := state.service.OpenToolSet(requestContext, "remote:one"); err == nil {
		t.Fatal("duplicate Tool Set identity was accepted")
	}
	if err := toolSet.Replace(
		requestContext,
		toolSetDefinitions("alpha", "beta"),
	); err != nil {
		t.Fatal(err)
	}
	if actual := schemaNames(state.service); !reflect.DeepEqual(
		actual,
		[]string{
			"alpha",
			"beta",
		},
	) {
		t.Fatalf("first Tool Set = %#v", actual)
	}
	if changes.Load() != 1 {
		t.Fatalf("first Replace published %d changes", changes.Load())
	}
	if err := toolSet.Replace(
		requestContext,
		toolSetDefinitions("gamma", "delta"),
	); err != nil {
		t.Fatal(err)
	}
	if actual := schemaNames(state.service); !reflect.DeepEqual(
		actual,
		[]string{
			"gamma",
			"delta",
		},
	) {
		t.Fatalf("second Tool Set = %#v", actual)
	}
	if changes.Load() != 2 {
		t.Fatalf("second Replace published %d changes", changes.Load())
	}
	if err := toolSet.Close(requestContext); err != nil {
		t.Fatal(err)
	}
	if err := toolSet.Close(requestContext); err != nil {
		t.Fatal(err)
	}
	if actual := schemaNames(state.service); len(actual) != 0 {
		t.Fatalf("closed Tool Set left %#v", actual)
	}
	if changes.Load() != 3 {
		t.Fatalf("Close published %d total changes, want 3", changes.Load())
	}
}

func TestToolSetBuildFailurePreservesCurrentGeneration(t *testing.T) {
	state := newToolsFixture(t)
	requestContext := context.Background()
	toolSet, err := state.service.OpenToolSet(requestContext, "remote:build")
	if err != nil {
		t.Fatal(err)
	}
	if err := toolSet.Replace(
		requestContext,
		toolSetDefinitions("current"),
	); err != nil {
		t.Fatal(err)
	}
	invalid := objectTool(
		"invalid",
		"invalid schema",
		tools.ExecutorFunc(passThroughBody),
	)
	invalid.Parameters = nil
	if err := toolSet.Replace(
		requestContext,
		[]tools.ToolDefinition{
			invalid,
		},
	); err == nil {
		t.Fatal("invalid candidate generation was accepted")
	}
	if actual := schemaNames(state.service); !reflect.DeepEqual(
		actual,
		[]string{
			"current",
		},
	) {
		t.Fatalf("build failure changed Tool Set to %#v", actual)
	}
	duplicates := toolSetDefinitions("duplicate", "duplicate")
	if err := toolSet.Replace(requestContext, duplicates); err == nil {
		t.Fatal("duplicate candidate generation was accepted")
	}
	if actual := schemaNames(state.service); !reflect.DeepEqual(
		actual,
		[]string{
			"current",
		},
	) {
		t.Fatalf("duplicate generation changed Tool Set to %#v", actual)
	}
}

func TestToolSetForeignConflictClearsOnlyOwnedGeneration(t *testing.T) {
	state := newToolsFixture(t)
	requestContext := context.Background()
	toolSet, err := state.service.OpenToolSet(requestContext, "remote:conflict")
	if err != nil {
		t.Fatal(err)
	}
	if err := toolSet.Replace(
		requestContext,
		toolSetDefinitions("owned"),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := state.service.AddTool(
		requestContext,
		toolSetDefinitions("foreign")[0],
	); err != nil {
		t.Fatal(err)
	}
	err = toolSet.Replace(
		requestContext,
		toolSetDefinitions("candidate", "foreign"),
	)
	if err == nil || !stringsContain(err.Error(), "another owner") {
		t.Fatalf("Replace conflict error = %v", err)
	}
	if actual := schemaNames(state.service); !reflect.DeepEqual(
		actual,
		[]string{
			"foreign",
		},
	) {
		t.Fatalf("conflict result = %#v", actual)
	}
}

func stringsContain(value string, fragment string) bool {
	return len(fragment) == 0 || (len(value) >= len(fragment) &&
		findFragment(value, fragment))
}

func findFragment(value string, fragment string) bool {
	for index := 0; index+len(fragment) <= len(value); index++ {
		if value[index:index+len(fragment)] == fragment {
			return true
		}
	}
	return false
}

func TestToolSetObserverFailureRollsBackWholeGeneration(t *testing.T) {
	failure := errors.New("reject replacement")
	var changes atomic.Int32
	observer := &eventObserverPlugin{
		name: "reject-second-tool-set-change",
		subscriptions: []plugin.EventSubscription{
			plugin.EventOf[tools.RegistryChanged](),
		},
		observe: func(context.Context, plugin.Event) error {
			if changes.Add(1) == 2 {
				return failure
			}
			return nil
		},
	}
	state := newToolsFixture(t, observer)
	requestContext := context.Background()
	toolSet, err := state.service.OpenToolSet(requestContext, "remote:rollback")
	if err != nil {
		t.Fatal(err)
	}
	if err := toolSet.Replace(
		requestContext,
		toolSetDefinitions("stable-one", "stable-two"),
	); err != nil {
		t.Fatal(err)
	}
	err = toolSet.Replace(
		requestContext,
		toolSetDefinitions("rejected-one", "rejected-two"),
	)
	if !errors.Is(err, failure) {
		t.Fatalf("Replace error = %v, want %v", err, failure)
	}
	if actual := schemaNames(state.service); !reflect.DeepEqual(
		actual,
		[]string{
			"stable-one",
			"stable-two",
		},
	) {
		t.Fatalf("observer failure left Tool Set %#v", actual)
	}
}

func TestToolSetStaleHandleCannotChangeReopenedIdentity(t *testing.T) {
	state := newToolsFixture(t)
	requestContext := context.Background()
	oldHandle, err := state.service.OpenToolSet(requestContext, "remote:reload")
	if err != nil {
		t.Fatal(err)
	}
	if err := oldHandle.Replace(
		requestContext,
		toolSetDefinitions("old"),
	); err != nil {
		t.Fatal(err)
	}
	if err := oldHandle.Close(requestContext); err != nil {
		t.Fatal(err)
	}
	newHandle, err := state.service.OpenToolSet(requestContext, "remote:reload")
	if err != nil {
		t.Fatal(err)
	}
	if err := newHandle.Replace(
		requestContext,
		toolSetDefinitions("new"),
	); err != nil {
		t.Fatal(err)
	}
	if err := oldHandle.Replace(
		requestContext,
		toolSetDefinitions("stale"),
	); !errors.Is(err, tools.ErrToolSetInactive) {
		t.Fatalf("stale Replace error = %v", err)
	}
	if err := oldHandle.Close(requestContext); err != nil {
		t.Fatal(err)
	}
	if actual := schemaNames(state.service); !reflect.DeepEqual(
		actual,
		[]string{
			"new",
		},
	) {
		t.Fatalf("stale handle changed reopened Tool Set to %#v", actual)
	}
}

func TestToolSetReadersNeverObservePartialGeneration(t *testing.T) {
	state := newToolsFixture(t)
	requestContext := context.Background()
	toolSet, err := state.service.OpenToolSet(requestContext, "remote:atomic")
	if err != nil {
		t.Fatal(err)
	}
	first := toolSetDefinitions("alpha", "beta")
	second := toolSetDefinitions("gamma", "delta")
	if err := toolSet.Replace(requestContext, first); err != nil {
		t.Fatal(err)
	}
	writerDone := make(chan error, 1)
	go func() {
		for iteration := 0; iteration < 200; iteration++ {
			candidate := first
			if iteration%2 == 0 {
				candidate = second
			}
			if replaceErr := toolSet.Replace(requestContext, candidate); replaceErr != nil {
				writerDone <- replaceErr
				return
			}
		}
		writerDone <- nil
	}()
	for {
		select {
		case err := <-writerDone:
			if err != nil {
				t.Fatal(err)
			}
			return
		default:
			names := schemaNames(state.service)
			if !reflect.DeepEqual(
				names,
				[]string{
					"alpha",
					"beta",
				},
			) && !reflect.DeepEqual(
				names,
				[]string{
					"gamma",
					"delta",
				},
			) {
				t.Fatalf("reader observed partial generation %#v", names)
			}
		}
	}
}

