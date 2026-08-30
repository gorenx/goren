package subagent_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentloop"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
	boundcontract "github.com/gorenx/goren/subagent/bound"
	"github.com/gorenx/goren/subagent/bound/turnrelay"
	subagentplugin "github.com/gorenx/goren/subagent/plugin"
	"github.com/gorenx/goren/subagent/spawn"
	subagentdelegation "github.com/gorenx/goren/subagent/tools/delegation"
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

type integrationObserverFailureSink struct {
	mutex    sync.Mutex
	failures []error
}

func (sink *integrationObserverFailureSink) report(problem error) {
	sink.mutex.Lock()
	sink.failures = append(sink.failures, problem)
	sink.mutex.Unlock()
}

func (sink *integrationObserverFailureSink) snapshot() []error {
	sink.mutex.Lock()
	defer sink.mutex.Unlock()
	return append([]error(nil), sink.failures...)
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
	mutex           sync.Mutex
	responses       [][]llm.StreamChunk
	gates           []<-chan struct{}
	requests        []llm.GenerateOptions
	requestsChanged chan struct{}
}

func (backend *integrationAdapter) Stream(
	requestContext context.Context,
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
	var gate <-chan struct{}
	if requestIndex < len(backend.gates) {
		gate = backend.gates[requestIndex]
	}
	requestsChanged := backend.requestsChanged
	backend.mutex.Unlock()
	if requestsChanged != nil {
		select {
		case requestsChanged <- struct{}{}:
		default:
		}
	}
	if gate != nil {
		select {
		case <-requestContext.Done():
			return nil, context.Cause(requestContext)
		case <-gate:
		}
	}
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

func (backend *integrationAdapter) setGates(gates []<-chan struct{}) {
	backend.mutex.Lock()
	backend.gates = append([]<-chan struct{}(nil), gates...)
	backend.mutex.Unlock()
}

func (backend *integrationAdapter) waitForRequests(
	requestContext context.Context,
	count int,
) error {
	for {
		backend.mutex.Lock()
		observed := len(backend.requests)
		requestsChanged := backend.requestsChanged
		backend.mutex.Unlock()
		if observed >= count {
			return nil
		}
		if requestsChanged == nil {
			return errors.New("integration adapter request notification is unavailable")
		}
		select {
		case <-requestContext.Done():
			return context.Cause(requestContext)
		case <-requestsChanged:
		}
	}
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
	runtimeEngine    *plugin.Runtime
	subagentHandle   plugin.Handle
	turnRelayHandle  plugin.Handle
	agents           *agent.RegistryService
	sessions         session.LiveStore
	toolRuntime      tools.ToolRuntime
	backend          *integrationAdapter
	lifecycle        *subagentLifecycleObserver
	eventFailures    *integrationEventFailureSink
	observerErrors   *integrationObserverFailureSink
	boundDefinitions boundcontract.Definitions
	parentOptions    agent.Options
}

type integrationBoundProbe struct {
	plugin.Base
	definitions boundcontract.Definitions
}

func (*integrationBoundProbe) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "subagent-integration-bound-probe",
		Requires: []plugin.ServiceType{
			plugin.ServiceOf[boundcontract.Definitions](),
		},
	}
}

func (probe *integrationBoundProbe) Apply(context.Context) error {
	definitions, err := plugin.Require[boundcontract.Definitions](probe)
	if err != nil {
		return err
	}
	probe.definitions = definitions
	return nil
}

func (*integrationBoundProbe) Dispose(context.Context) error { return nil }

type integrationSessionStoreProbe struct {
	plugin.Base
	store session.LiveStore
}

func (*integrationSessionStoreProbe) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "subagent-integration-session-store-probe",
		Requires: []plugin.ServiceType{
			plugin.ServiceOf[session.LiveStore](),
		},
	}
}

func (probe *integrationSessionStoreProbe) Apply(context.Context) error {
	liveStore, err := plugin.Require[session.LiveStore](probe)
	if err != nil {
		return err
	}
	probe.store = liveStore
	return nil
}

func (*integrationSessionStoreProbe) Dispose(context.Context) error { return nil }

type integrationConfiguration struct {
	agentOptions agent.Options
	plugins      []plugin.Plugin
	backend      *integrationAdapter
	delegation   subagentdelegation.Settings
}

func newIntegrationFixture(
	t *testing.T,
	responses [][]llm.StreamChunk,
	additionalPlugins ...plugin.Plugin,
) *integrationFixture {
	t.Helper()
	durability := newIntegrationDurability(t)
	configuredPlugins := append(
		[]plugin.Plugin(nil),
		durability.plugins...,
	)
	configuredPlugins = append(configuredPlugins, additionalPlugins...)
	return newIntegrationFixtureWithConfiguration(
		t,
		integrationConfiguration{
			agentOptions: agent.Options{
				Provider: "mock",
				Model:    "model",
			},
			backend: &integrationAdapter{
				responses:       responses,
				requestsChanged: make(chan struct{}, 1),
			},
			plugins: configuredPlugins,
			delegation: subagentdelegation.Settings{
				SeedBuilder:           spawn.DefaultSeedBuilderName,
				ToolName:              subagentdelegation.DefaultToolName,
				EnableRunInBackground: false,
				BackgroundMode:        subagentdelegation.BackgroundOneShot,
			},
		},
	)
}

func newIntegrationFixtureWithConfiguration(
	t *testing.T,
	configuration integrationConfiguration,
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
	loopPlugin, loopErr := agentloop.NewPlugin(
		agentloop.Settings{
			MaxParallelToolCalls: agentloop.DefaultMaxParallelToolCalls,
		},
		agentloop.RuntimeOptions{},
	)
	if loopErr != nil {
		t.Fatal(loopErr)
	}
	sessionPlugin, storeErr := session.NewPlugin(session.MemoryStoreOptions{
		PostCommitFailures: integrationPostCommitFailureSink{},
	})
	if storeErr != nil {
		t.Fatal(storeErr)
	}
	storeProbe := &integrationSessionStoreProbe{}
	agentRegistry := agent.NewRegistry(agent.RegistryOptions{})
	agentPlugin, registryErr := agent.NewRegistryPlugin(agentRegistry)
	if registryErr != nil {
		t.Fatal(registryErr)
	}
	modelRuntime := llm.NewRuntime(nil)
	promptRuntime := systemprompt.New(
		promptSettings,
		systemprompt.RegistryOptions{},
	)
	toolService := tools.New(toolSettings)
	spawnPlugin, pluginErr := spawn.NewPlugin(spawn.DefaultSeedBuilderName)
	if pluginErr != nil {
		t.Fatal(pluginErr)
	}
	delegationTool, toolErr := subagentdelegation.New(configuration.delegation)
	if toolErr != nil {
		t.Fatal(toolErr)
	}
	lifecycle := &subagentLifecycleObserver{
		ended: make(chan struct{}, 8),
	}
	eventFailures := &integrationEventFailureSink{}
	observerErrors := &integrationObserverFailureSink{}
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{
		EventFailures: eventFailures,
	})
	rootPlugins := []plugin.Plugin{
		lifecycle,
		agentPlugin,
		sessionPlugin,
		storeProbe,
		modelRuntime,
	}
	rootPlugins = append(rootPlugins, configuration.plugins...)
	subagentPlugin := subagentplugin.New(subagentplugin.Diagnostics{
		ObserverError: observerErrors.report,
	}, subagentplugin.DefinitionDatabase{
		Path: filepath.Join(
			t.TempDir(),
			"bound-definitions.sqlite",
		),
		JournalMode: subagentplugin.JournalWAL,
	})
	boundProbe := &integrationBoundProbe{}
	turnRelayPlugin := turnrelay.New(turnrelay.Diagnostics{
		WorkerError: observerErrors.report,
	})
	rootPlugins = append(
		rootPlugins,
		promptRuntime,
		toolService,
	)
	subagentPluginIndex := len(rootPlugins)
	rootPlugins = append(
		rootPlugins,
		subagentPlugin,
		boundProbe,
	)
	turnRelayPluginIndex := len(rootPlugins)
	rootPlugins = append(
		rootPlugins,
		turnRelayPlugin,
		spawnPlugin,
		delegationTool,
		loopPlugin,
	)
	handles, startErr := runtimeEngine.Start(
		context.Background(),
		rootPlugins...,
	)
	if startErr != nil {
		t.Fatal(startErr)
	}
	t.Cleanup(func() {
		if shutdownErr := agentRegistry.Shutdown(context.Background()); shutdownErr != nil {
			t.Errorf("Agent Registry shutdown failed: %v", shutdownErr)
		}
		if shutdownErr := runtimeEngine.Shutdown(context.Background()); shutdownErr != nil {
			t.Errorf(
				"Runtime shutdown failed: %v; details: %#v",
				shutdownErr,
				flattenIntegrationErrors(shutdownErr),
			)
		}
	})
	if configuration.backend != nil {
		adapterHandle, adapterErr := modelRuntime.RegisterAdapter(
			context.Background(),
			[]string{configuration.agentOptions.Provider},
			configuration.backend,
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
		runtimeEngine:    runtimeEngine,
		subagentHandle:   handles[subagentPluginIndex],
		turnRelayHandle:  handles[turnRelayPluginIndex],
		agents:           agentRegistry,
		sessions:         storeProbe.store,
		toolRuntime:      toolService,
		backend:          configuration.backend,
		lifecycle:        lifecycle,
		eventFailures:    eventFailures,
		observerErrors:   observerErrors,
		boundDefinitions: boundProbe.definitions,
		parentOptions:    configuration.agentOptions,
	}
}

func flattenIntegrationErrors(problem error) []string {
	if problem == nil {
		return nil
	}
	type manyCauses interface {
		Unwrap() []error
	}
	if joined, matches := problem.(manyCauses); matches {
		flattened := make([]string, 0)
		for _, cause := range joined.Unwrap() {
			flattened = append(flattened, flattenIntegrationErrors(cause)...)
		}
		return flattened
	}
	if cause := errors.Unwrap(problem); cause != nil {
		return append(
			[]string{fmt.Sprintf("%T: %v", problem, problem)},
			flattenIntegrationErrors(cause)...,
		)
	}
	return []string{fmt.Sprintf("%T: %v", problem, problem)}
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
