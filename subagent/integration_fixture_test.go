package subagent_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentloop"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
	subagentruntime "github.com/gorenx/goren/subagent/runtime"
	"github.com/gorenx/goren/subagent/spawn"
	subagenttool "github.com/gorenx/goren/subagent/tool"
	"github.com/gorenx/goren/systemprompt"
	"github.com/gorenx/goren/tools"
)

type integrationPostCommitFailureSink struct{}

func (integrationPostCommitFailureSink) ReportPostCommitFailure(
	session.PostCommitFailure,
) {
}

type integrationEventFailureSink struct {
	mutex    sync.Mutex
	failures []plugin.EventFailure
}

func (sink *integrationEventFailureSink) ReportEventFailure(
	_ context.Context,
	failure plugin.EventFailure,
) {
	sink.mutex.Lock()
	sink.failures = append(sink.failures, failure)
	sink.mutex.Unlock()
}

func (sink *integrationEventFailureSink) snapshot() []plugin.EventFailure {
	sink.mutex.Lock()
	defer sink.mutex.Unlock()
	return append([]plugin.EventFailure(nil), sink.failures...)
}

type integrationAdapter struct {
	mutex     sync.Mutex
	responses [][]llm.StreamChunk
	requests  []llm.GenerateOptions
}

func (backend *integrationAdapter) Stream(
	_ context.Context,
	requestOptions llm.GenerateOptions,
) (llm.ChunkStream, error) {
	backend.mutex.Lock()
	requestSnapshot, cloneErr := llm.CloneGenerateOptions(requestOptions)
	if cloneErr != nil {
		backend.mutex.Unlock()
		return nil, cloneErr
	}
	backend.requests = append(backend.requests, requestSnapshot)
	requestIndex := len(backend.requests) - 1
	if requestIndex >= len(backend.responses) {
		backend.mutex.Unlock()
		return nil, errors.New("integration adapter has no scripted response")
	}
	response := backend.responses[requestIndex]
	backend.mutex.Unlock()
	return llm.NewSliceStream(response)
}

func (backend *integrationAdapter) snapshots() []llm.GenerateOptions {
	backend.mutex.Lock()
	defer backend.mutex.Unlock()
	result := make([]llm.GenerateOptions, 0, len(backend.requests))
	for _, requestSnapshot := range backend.requests {
		detached, _ := llm.CloneGenerateOptions(requestSnapshot)
		result = append(result, detached)
	}
	return result
}

type subagentLifecycleObserver struct {
	plugin.Base
	mutex  sync.Mutex
	starts []subagent.Started
	ends   []subagent.Ended
	ended  chan struct{}
}

func (*subagentLifecycleObserver) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "subagent-integration-lifecycle-observer",
		Events: []plugin.EventSubscription{
			plugin.EventOf[subagent.Started](),
			plugin.EventOf[subagent.Ended](),
		},
	}
}

func (*subagentLifecycleObserver) Apply(requestContext context.Context) error {
	return requestContext.Err()
}

func (*subagentLifecycleObserver) Dispose(context.Context) error {
	return nil
}

func (observerState *subagentLifecycleObserver) ObserveEvent(
	_ context.Context,
	fact plugin.Event,
) error {
	observerState.mutex.Lock()
	defer observerState.mutex.Unlock()
	switch eventValue := fact.(type) {
	case subagent.Started:
		observerState.starts = append(observerState.starts, eventValue)
	case subagent.Ended:
		observerState.ends = append(observerState.ends, eventValue)
		observerState.ended <- struct{}{}
	}
	return nil
}

func (observerState *subagentLifecycleObserver) waitForEnd(
	requestContext context.Context,
) error {
	select {
	case <-requestContext.Done():
		return context.Cause(requestContext)
	case <-observerState.ended:
		return nil
	}
}

func (observerState *subagentLifecycleObserver) snapshot() (
	[]subagent.Started,
	[]subagent.Ended,
) {
	observerState.mutex.Lock()
	defer observerState.mutex.Unlock()
	return append([]subagent.Started(nil), observerState.starts...),
		append([]subagent.Ended(nil), observerState.ends...)
}

type integrationFixture struct {
	agents        *agent.RegistryPlugin
	sessions      *session.MemoryStore
	toolRuntime   tools.ToolRuntime
	backend       *integrationAdapter
	lifecycle     *subagentLifecycleObserver
	eventFailures *integrationEventFailureSink
	parentOptions agent.Options
}

type integrationModel struct {
	options agent.Options
	plugins []plugin.Plugin
	backend *integrationAdapter
}

func newIntegrationFixture(
	t *testing.T,
	responses [][]llm.StreamChunk,
) *integrationFixture {
	t.Helper()
	return newIntegrationFixtureWithModel(
		t,
		integrationModel{
			options: agent.Options{
				Provider: "mock",
				Model:    "model",
			},
			backend: &integrationAdapter{
				responses: responses,
			},
		},
	)
}

func newIntegrationFixtureWithModel(
	t *testing.T,
	selectedModel integrationModel,
) *integrationFixture {
	t.Helper()
	promptSettings, settingsErr := systemprompt.ValidateConfig(systemprompt.Config{})
	if settingsErr != nil {
		t.Fatal(settingsErr)
	}
	toolSettings, settingsErr := tools.ValidateConfig(tools.Config{})
	if settingsErr != nil {
		t.Fatal(settingsErr)
	}
	loopPlugin, loopErr := agentloop.New(
		agentloop.Settings{
			MaxParallelToolCalls: agentloop.DefaultMaxParallelToolCalls,
		},
		agentloop.RuntimeOptions{},
	)
	if loopErr != nil {
		t.Fatal(loopErr)
	}
	sessionStore, storeErr := session.NewMemoryStore(session.MemoryStoreOptions{
		PostCommitFailures: integrationPostCommitFailureSink{},
	})
	if storeErr != nil {
		t.Fatal(storeErr)
	}
	agentRegistry := agent.NewRegistry(agent.RegistryOptions{})
	modelRuntime := llm.NewRuntime(nil)
	promptRuntime := systemprompt.New(
		promptSettings,
		systemprompt.RegistryOptions{},
	)
	toolService := tools.New(toolSettings)
	spawnProvider, providerErr := spawn.New(spawn.DefaultProviderName)
	if providerErr != nil {
		t.Fatal(providerErr)
	}
	delegationTool, toolErr := subagenttool.New(subagenttool.Settings{
		Provider:              spawn.DefaultProviderName,
		ToolName:              subagenttool.DefaultToolName,
		EnableRunInBackground: false,
		BackgroundMode:        subagenttool.BackgroundOneShot,
	})
	if toolErr != nil {
		t.Fatal(toolErr)
	}
	lifecycle := &subagentLifecycleObserver{
		ended: make(chan struct{}, 8),
	}
	eventFailures := &integrationEventFailureSink{}
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{
		EventFailures: eventFailures,
	})
	rootPlugins := []plugin.Plugin{
		lifecycle,
		agentRegistry,
		sessionStore,
		modelRuntime,
	}
	rootPlugins = append(rootPlugins, selectedModel.plugins...)
	rootPlugins = append(
		rootPlugins,
		promptRuntime,
		toolService,
		subagentruntime.New(),
		spawnProvider,
		delegationTool,
		loopPlugin,
	)
	if _, startErr := runtimeEngine.Start(
		context.Background(),
		rootPlugins...,
	); startErr != nil {
		t.Fatal(startErr)
	}
	t.Cleanup(func() {
		if shutdownErr := runtimeEngine.Shutdown(context.Background()); shutdownErr != nil {
			t.Error(shutdownErr)
		}
	})
	if selectedModel.backend != nil {
		adapterHandle, adapterErr := modelRuntime.RegisterAdapter(
			context.Background(),
			[]string{selectedModel.options.Provider},
			selectedModel.backend,
		)
		if adapterErr != nil {
			t.Fatal(adapterErr)
		}
		t.Cleanup(func() {
			if releaseErr := adapterHandle.Release(context.Background()); releaseErr != nil {
				t.Error(releaseErr)
			}
		})
	}
	return &integrationFixture{
		agents:        agentRegistry,
		sessions:      sessionStore,
		toolRuntime:   toolService,
		backend:       selectedModel.backend,
		lifecycle:     lifecycle,
		eventFailures: eventFailures,
		parentOptions: selectedModel.options,
	}
}

func (state *integrationFixture) createParent(t *testing.T) agent.Handle {
	t.Helper()
	handle, createErr := state.agents.Create(
		context.Background(),
		agent.CreateOptions{
			SessionID:    "integration-parent",
			AgentOptions: state.parentOptions,
		},
	)
	if createErr != nil {
		t.Fatal(createErr)
	}
	t.Cleanup(func() {
		if disposeErr := handle.Dispose(context.Background()); disposeErr != nil {
			t.Error(disposeErr)
		}
	})
	return handle
}
