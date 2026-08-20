package tools_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/systemprompt"
	"github.com/gorenx/goren/tools"
)

type eventFailureRecorder struct {
	mutex    sync.Mutex
	failures []plugin.EventFailure
}

func (recorder *eventFailureRecorder) ReportEventFailure(
	_ context.Context,
	failure plugin.EventFailure,
) {
	recorder.mutex.Lock()
	recorder.failures = append(recorder.failures, failure)
	recorder.mutex.Unlock()
}

type toolsFixture struct {
	runtimeEngine *plugin.Runtime
	prompts       *systemprompt.Registry
	promptHandle  plugin.Handle
	service       *tools.Service
	failures      *eventFailureRecorder
}

func newToolsFixture(
	testingContext *testing.T,
	plugins ...plugin.Plugin,
) *toolsFixture {
	testingContext.Helper()
	promptSettings, err := systemprompt.ValidateConfig(systemprompt.Config{})
	if err != nil {
		testingContext.Fatal(err)
	}
	toolSettings, err := tools.ValidateConfig(tools.Config{})
	if err != nil {
		testingContext.Fatal(err)
	}
	prompts := systemprompt.New(
		promptSettings,
		systemprompt.RegistryOptions{},
	)
	service := tools.New(toolSettings)
	allPlugins := []plugin.Plugin{
		prompts,
		service,
	}
	allPlugins = append(allPlugins, plugins...)
	failures := &eventFailureRecorder{}
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{
		EventFailures: failures,
	})
	handles, err := runtimeEngine.Start(
		context.Background(),
		allPlugins...,
	)
	if err != nil {
		testingContext.Fatal(err)
	}
	state := &toolsFixture{
		runtimeEngine: runtimeEngine,
		prompts:       prompts,
		promptHandle:  handles[0],
		service:       service,
		failures:      failures,
	}
	testingContext.Cleanup(func() {
		if err := runtimeEngine.Shutdown(context.Background()); err != nil {
			testingContext.Error(err)
		}
	})
	return state
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
			Renderer: tools.OutputRendererFunc(func(
				_ json.RawMessage,
				value json.RawMessage,
			) ([]llm.ContentBlock, error) {
				return []llm.ContentBlock{
					llm.NewTextBlock(string(value)),
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

type eventObserverPlugin struct {
	plugin.Base
	name          string
	subscriptions []plugin.EventSubscription
	observe       func(context.Context, plugin.Event) error
}

func (observer *eventObserverPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name:   observer.name,
		Events: observer.subscriptions,
	}
}

func (*eventObserverPlugin) Apply(context.Context) error {
	return nil
}

func (*eventObserverPlugin) Dispose(context.Context) error {
	return nil
}

func (observer *eventObserverPlugin) ObserveEvent(
	requestContext context.Context,
	fact plugin.Event,
) error {
	return observer.observe(requestContext, fact)
}

func TestServiceExposesFocusedCapabilities(t *testing.T) {
	t.Parallel()
	settings, err := tools.ValidateConfig(tools.Config{})
	if err != nil {
		t.Fatal(err)
	}
	service := tools.New(settings)
	if _, matches := any(service).(tools.ToolRuntime); !matches {
		t.Fatal("Service does not implement ToolRuntime")
	}
	if _, matches := any(service).(tools.ToolCatalog); !matches {
		t.Fatal("Service does not implement ToolCatalog")
	}
	if _, matches := any(service).(tools.PolicyRegistry); !matches {
		t.Fatal("Service does not implement PolicyRegistry")
	}
	if _, matches := any(service).(tools.ToolExecutionScheduler); matches {
		t.Fatal("Service exposes staged scheduling outside Scheduler")
	}
	if service.Scheduler() != nil {
		t.Fatal("inactive Service returned a Scheduler")
	}
	if err := service.AddGuard(
		context.Background(),
		"inactive",
		tools.ToolGuardFunc(func(tools.ToolExecution) (string, bool) {
			return "", false
		}),
	); !errors.Is(err, plugin.ErrPluginNotActive) {
		t.Fatalf("inactive AddGuard error = %v", err)
	}
	serviceManifest := service.Manifest()
	actual := make([]string, 0, len(serviceManifest.Provides))
	for _, provided := range serviceManifest.Provides {
		actual = append(actual, provided.Name())
	}
	want := []string{
		"github.com/gorenx/goren/tools.ToolRuntime",
		"github.com/gorenx/goren/tools.ToolCatalog",
		"github.com/gorenx/goren/tools.PolicyRegistry",
	}
	if !reflect.DeepEqual(actual, want) {
		t.Fatalf("provided Services = %#v, want %#v", actual, want)
	}
}

func TestRegistryLayersRestrictionShadowingAndPromptProjection(t *testing.T) {
	state := newToolsFixture(t)
	requestContext := context.Background()
	for _, definition := range []tools.ToolDefinition{
		objectTool(
			"alpha",
			"global alpha",
			tools.ExecutorFunc(passThroughBody),
		),
		objectTool(
			"beta",
			"global beta",
			tools.ExecutorFunc(passThroughBody),
		),
	} {
		if err := state.service.AddTool(requestContext, definition); err != nil {
			t.Fatal(err)
		}
	}

	promptOverlay := systemprompt.NewOverlay(systemprompt.RegistryOptions{})
	promptOverlayHandle, err := state.runtimeEngine.MountScopedChild(
		requestContext,
		state.promptHandle,
		promptOverlay,
	)
	if err != nil {
		t.Fatal(err)
	}
	toolOverlay := tools.NewOverlay()
	if _, err := state.runtimeEngine.MountScopedChild(
		requestContext,
		promptOverlayHandle,
		toolOverlay,
	); err != nil {
		t.Fatal(err)
	}
	if err := toolOverlay.AddRestriction(
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
	if err := toolOverlay.AddTool(
		requestContext,
		objectTool(
			"beta",
			"scoped beta",
			tools.ExecutorFunc(passThroughBody),
		),
	); err != nil {
		t.Fatal(err)
	}
	if err := toolOverlay.AddTool(
		requestContext,
		objectTool(
			"gamma",
			"scoped gamma",
			tools.ExecutorFunc(passThroughBody),
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
	assembled, err := promptOverlay.Assemble(
		requestContext,
		systemprompt.AssembleContext{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(assembled.Tools) != 3 || assembled.Tools[1].Name != "beta" {
		t.Fatalf("assembled tools = %#v", assembled.Tools)
	}
	assembled.Tools[0].Parameters[0] = '['
	if toolOverlay.Schemas()[0].Parameters[0] != '{' {
		t.Fatal("schema projection aliases Registry storage")
	}
}

func TestRegistryChangeFailureRollsBackMutation(t *testing.T) {
	failure := errors.New("change rejected")
	observer := &eventObserverPlugin{
		name: "reject-tool-change",
		subscriptions: []plugin.EventSubscription{
			plugin.EventOf[tools.RegistryChanged](),
		},
		observe: func(context.Context, plugin.Event) error {
			return failure
		},
	}
	state := newToolsFixture(t, observer)
	err := state.service.AddTool(
		context.Background(),
		objectTool(
			"rolled-back",
			"",
			tools.ExecutorFunc(passThroughBody),
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
	observer := &eventObserverPlugin{
		name: "fail-tool-removal-change",
		subscriptions: []plugin.EventSubscription{
			plugin.EventOf[tools.RegistryChanged](),
		},
		observe: func(context.Context, plugin.Event) error {
			if notifications.Add(1) == 2 {
				return failure
			}
			return nil
		},
	}
	state := newToolsFixture(t, observer)
	if err := state.service.AddTool(
		context.Background(),
		objectTool(
			"removed-despite-observer",
			"",
			tools.ExecutorFunc(passThroughBody),
		),
	); err != nil {
		t.Fatal(err)
	}
	err := state.service.RemoveTool(
		context.Background(),
		"removed-despite-observer",
	)
	if !errors.Is(err, failure) {
		t.Fatalf("RemoveTool error = %v", err)
	}
	if _, found := state.service.Get("removed-despite-observer"); found {
		t.Fatal("failed removal notification restored a stopped contribution")
	}
}

func TestAncestorRestrictionDoesNotFilterSameLayerToolsInDescendant(t *testing.T) {
	state := newToolsFixture(t)
	requestContext := context.Background()
	for _, definition := range []tools.ToolDefinition{
		objectTool(
			"alpha",
			"root alpha",
			tools.ExecutorFunc(passThroughBody),
		),
		objectTool(
			"beta",
			"root beta",
			tools.ExecutorFunc(passThroughBody),
		),
	} {
		if err := state.service.AddTool(requestContext, definition); err != nil {
			t.Fatal(err)
		}
	}
	firstPrompt := systemprompt.NewOverlay(systemprompt.RegistryOptions{})
	firstPromptHandle, err := state.runtimeEngine.MountScopedChild(
		requestContext,
		state.promptHandle,
		firstPrompt,
	)
	if err != nil {
		t.Fatal(err)
	}
	firstTools := tools.NewOverlay()
	firstToolsHandle, err := state.runtimeEngine.MountScopedChild(
		requestContext,
		firstPromptHandle,
		firstTools,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := firstTools.AddRestriction(
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
	if err := firstTools.AddTool(
		requestContext,
		objectTool(
			"beta",
			"child beta",
			tools.ExecutorFunc(passThroughBody),
		),
	); err != nil {
		t.Fatal(err)
	}
	secondPrompt := systemprompt.NewOverlay(systemprompt.RegistryOptions{})
	secondPromptHandle, err := state.runtimeEngine.MountScopedChild(
		requestContext,
		firstToolsHandle,
		secondPrompt,
	)
	if err != nil {
		t.Fatal(err)
	}
	secondTools := tools.NewOverlay()
	if _, err := state.runtimeEngine.MountScopedChild(
		requestContext,
		secondPromptHandle,
		secondTools,
	); err != nil {
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
