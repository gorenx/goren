package tools_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/tools"
)

func TestToolHandleCannotRemoveSameNameFromAnotherLayer(t *testing.T) {
	t.Parallel()
	state := newToolsFixture(t)
	oldHandle, err := state.service.AddTool(
		context.Background(),
		objectTool(
			"activation-owned",
			"old activation",
			executorStub(passThroughBody),
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.service.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	state = newToolsFixture(t)
	if _, err = state.service.AddTool(
		context.Background(),
		objectTool(
			"activation-owned",
			"new activation",
			executorStub(passThroughBody),
		),
	); err != nil {
		t.Fatal(err)
	}
	if err = oldHandle.Unregister(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = oldHandle.Unregister(context.Background()); err != nil {
		t.Fatal(err)
	}
	definition, found := state.service.Get("activation-owned")
	if !found || definition.Description != "new activation" {
		t.Fatalf("Tool after stale-handle removal = (%#v, %t)", definition, found)
	}
}

func objectTool(
	name string,
	description string,
	body tools.Executor,
) tools.ToolDefinition {
	return tools.ToolDefinition{
		Name:        name,
		Description: description,
		Parameters:  json.RawMessage(`{"type":"object"}`),
		Output: tools.ToolOutputDefinition{
			Schema: json.RawMessage(`{"type":"object"}`),
			Renderer: outputRendererStub(func(
				_ json.RawMessage,
				value json.RawMessage,
			) ([]agentmessage.ContentBlock, error) {
				return []agentmessage.ContentBlock{
					agentmessage.NewTextBlock(string(value)),
				}, nil
			}),
		},
		Executor: body,
	}
}

func passThroughBody(
	arguments json.RawMessage,
	_ tools.ToolRunContext,
) (json.RawMessage, error) {
	return append(json.RawMessage(nil), arguments...), nil
}

func TestConstructionAndClose(t *testing.T) {
	if _, err := tools.New(tools.ValidatedConfig{}, tools.Options{}); err == nil {
		t.Fatal("unvalidated configuration accepted")
	}
	state := newToolsFixture(t)
	if state.service.Scheduler() == nil {
		t.Fatal("ready layer has no scheduler")
	}
	if err := state.service.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if state.service.Scheduler() != nil {
		t.Fatal("closed layer returned scheduler")
	}
	if _, err := state.service.AddGuard(context.Background(), "closed", tools.ToolGuardFunc(func(tools.ToolExecution) (string, bool) {
		return "", false
	})); !errors.Is(err, tools.ErrToolLayerInactive) {
		t.Fatalf("closed AddGuard: %v", err)
	}
}

func TestRegistryLayersRestrictionShadowingAndDefinitions(t *testing.T) {
	state := newToolsFixture(t)
	requestContext := context.Background()
	for _, definition := range []tools.ToolDefinition{
		objectTool(
			"alpha",
			"global alpha",
			executorStub(passThroughBody),
		),
		objectTool(
			"beta",
			"global beta",
			executorStub(passThroughBody),
		),
	} {
		if _, err := state.service.AddTool(requestContext, definition); err != nil {
			t.Fatal(err)
		}
	}

	toolOverlay, err := state.service.NewLayer(requestContext)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := toolOverlay.AddRestriction(
		requestContext,
		"agent-visible",
		tools.ToolRestriction{
			Allow: []string{
				"alpha",
			},
		},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := toolOverlay.AddTool(
		requestContext,
		objectTool(
			"beta",
			"scoped beta",
			executorStub(passThroughBody),
		),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := toolOverlay.AddTool(
		requestContext,
		objectTool(
			"gamma",
			"scoped gamma",
			executorStub(passThroughBody),
		),
	); err != nil {
		t.Fatal(err)
	}

	projections := toolOverlay.Schemas()
	wantNames := []string{
		"alpha",
		"beta",
		"gamma",
	}
	actualNames := make([]string, 0, len(projections))
	for _, schema := range projections {
		actualNames = append(actualNames, schema.Name)
	}
	if !reflect.DeepEqual(actualNames, wantNames) ||
		projections[1].Description != "scoped beta" {
		t.Fatalf("scoped schemas = %#v", projections)
	}
	if rootSchemas := state.service.Schemas(); len(rootSchemas) != 2 {
		t.Fatalf("root schemas = %#v", rootSchemas)
	}
	definitions, err := toolOverlay.CaptureDefinitions(requestContext)
	if err != nil {
		t.Fatal(err)
	}
	if len(definitions.Schemas) != 3 || definitions.Schemas[1].Name != "beta" {
		t.Fatalf("definition schemas = %#v", definitions.Schemas)
	}
	definitions.Schemas[0].Parameters[0] = '['
	if toolOverlay.Schemas()[0].Parameters[0] != '{' {
		t.Fatal("schema projection aliases Registry storage")
	}
}

func TestChildToolOverridesInheritedSchemaAndExecutorUntilUnregistered(
	t *testing.T,
) {
	state := newToolsFixture(t)
	requestContext := context.Background()
	if _, err := state.service.AddTool(
		requestContext,
		objectTool(
			"search",
			"root search",
			executorStub(func(
				json.RawMessage,
				tools.ToolRunContext,
			) (json.RawMessage, error) {
				return json.RawMessage(`{"source":"root"}`), nil
			}),
		),
	); err != nil {
		t.Fatal(err)
	}
	toolOverlay, err := state.service.NewLayer(requestContext)
	if err != nil {
		t.Fatal(err)
	}
	childHandle, err := toolOverlay.AddTool(
		requestContext,
		objectTool(
			"search",
			"child search",
			executorStub(func(
				json.RawMessage,
				tools.ToolRunContext,
			) (json.RawMessage, error) {
				return json.RawMessage(`{"source":"child"}`), nil
			}),
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	assertSearchTool(t, toolOverlay, "child search", `{"source":"child"}`)
	assertSearchTool(t, state.service, "root search", `{"source":"root"}`)

	if err = childHandle.Unregister(requestContext); err != nil {
		t.Fatal(err)
	}
	assertSearchTool(t, toolOverlay, "root search", `{"source":"root"}`)
}

func TestSameLayerDuplicateToolDoesNotReplaceFirstEntry(t *testing.T) {
	state := newToolsFixture(t)
	requestContext := context.Background()
	if _, err := state.service.AddTool(
		requestContext,
		objectTool(
			"search",
			"first",
			executorStub(passThroughBody),
		),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := state.service.AddTool(
		requestContext,
		objectTool(
			"search",
			"second",
			executorStub(passThroughBody),
		),
	); err == nil {
		t.Fatal("same-layer duplicate Tool registration succeeded")
	}
	definition, found := state.service.Get("search")
	if !found || definition.Description != "first" {
		t.Fatalf("Tool after duplicate registration = (%#v, %t)", definition, found)
	}
}

func assertSearchTool(
	t *testing.T,
	runtime tools.ToolRuntime,
	wantDescription string,
	wantValue string,
) {
	t.Helper()
	schemas := runtime.Schemas()
	if len(schemas) != 1 || schemas[0].Name != "search" ||
		schemas[0].Description != wantDescription {
		t.Fatalf("search schemas = %#v, want description %q", schemas, wantDescription)
	}
	outcome := runtime.Execute(
		context.Background(),
		tools.ToolExecutionInput{
			CallID:    "search-call",
			Name:      "search",
			Arguments: json.RawMessage(`{}`),
		},
	)
	value, succeeded := outcome.SuccessValue()
	if !succeeded || string(value) != wantValue {
		t.Fatalf("search result = %#v, want %s", outcome, wantValue)
	}
}

func TestOverlayCapturesParentDefinitionsAtCreation(t *testing.T) {
	state := newToolsFixture(t)
	ctx := context.Background()
	initial, err := state.service.AddTool(ctx, objectTool(
		"initial",
		"initial root tool",
		executorStub(passThroughBody),
	))
	if err != nil {
		t.Fatal(err)
	}
	child, err := state.service.NewLayer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.service.AddTool(ctx, objectTool(
		"added",
		"added after child creation",
		executorStub(passThroughBody),
	)); err != nil {
		t.Fatal(err)
	}
	if err := initial.Unregister(ctx); err != nil {
		t.Fatal(err)
	}
	if schemas := child.Schemas(); len(schemas) != 1 || schemas[0].Name != "initial" {
		t.Fatalf("parent mutation changed captured child: %#v", schemas)
	}
	if schemas := state.service.Schemas(); len(schemas) != 1 || schemas[0].Name != "added" {
		t.Fatalf("parent catalog did not change: %#v", schemas)
	}
	later, err := state.service.NewLayer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if schemas := later.Schemas(); len(schemas) != 1 || schemas[0].Name != "added" {
		t.Fatalf("new child did not capture current definitions: %#v", schemas)
	}
}

func TestRegistryChangeFailureRollsBackMutation(t *testing.T) {
	failure := errors.New("change rejected")
	observer := &eventObserver{
		name: "reject-tool-change",
		subscriptions: []string{
			tools.ChangeEventName,
		},
		observe: func(context.Context, toolEvent) error {
			return failure
		},
	}
	state := newToolsFixture(t, observer)
	_, err := state.service.AddTool(
		context.Background(),
		objectTool(
			"rolled-back",
			"",
			executorStub(passThroughBody),
		),
	)
	if !errors.Is(err, failure) {
		t.Fatalf("AddTool error = %v", err)
	}
	if _, found := state.service.Get("rolled-back"); found {
		t.Fatal("failed Registry mutation leaked")
	}
}

func TestRegistryRemovalSurvivesChangeObserverFailure(t *testing.T) {
	failure := errors.New("change observer failed during removal")
	var notifications atomic.Int32
	observer := &eventObserver{
		name: "fail-tool-removal-change",
		subscriptions: []string{
			tools.ChangeEventName,
		},
		observe: func(context.Context, toolEvent) error {
			if notifications.Add(1) == 2 {
				return failure
			}
			return nil
		},
	}
	state := newToolsFixture(t, observer)
	toolHandle, err := state.service.AddTool(
		context.Background(),
		objectTool(
			"removed-despite-observer",
			"",
			executorStub(passThroughBody),
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	err = toolHandle.Unregister(context.Background())
	if !errors.Is(err, failure) {
		t.Fatalf("Unregister error = %v", err)
	}
	if _, found := state.service.Get("removed-despite-observer"); found {
		t.Fatal("failed removal notification restored the removed Tool")
	}
}

func TestAncestorRestrictionDoesNotFilterSameLayerToolsInDescendant(t *testing.T) {
	state := newToolsFixture(t)
	requestContext := context.Background()
	for _, definition := range []tools.ToolDefinition{
		objectTool(
			"alpha",
			"root alpha",
			executorStub(passThroughBody),
		),
		objectTool(
			"beta",
			"root beta",
			executorStub(passThroughBody),
		),
	} {
		if _, err := state.service.AddTool(requestContext, definition); err != nil {
			t.Fatal(err)
		}
	}
	firstTools, err := state.service.NewLayer(requestContext)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := firstTools.AddRestriction(
		requestContext,
		"only-alpha-from-root",
		tools.ToolRestriction{
			Allow: []string{
				"alpha",
			},
		},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := firstTools.AddTool(
		requestContext,
		objectTool(
			"beta",
			"child beta",
			executorStub(passThroughBody),
		),
	); err != nil {
		t.Fatal(err)
	}
	secondTools, err := firstTools.NewLayer(requestContext)
	if err != nil {
		t.Fatal(err)
	}
	schemas := secondTools.Schemas()
	actualNames := make([]string, 0, len(schemas))
	for _, schema := range schemas {
		actualNames = append(actualNames, schema.Name)
	}
	wantNames := []string{
		"alpha",
		"beta",
	}
	if !reflect.DeepEqual(actualNames, wantNames) ||
		schemas[1].Description != "child beta" {
		t.Fatalf("descendant schemas = %#v", schemas)
	}
}
