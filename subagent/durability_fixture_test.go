package subagent_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session/persistence"
	persistencefactory "github.com/gorenx/goren/session/persistence/factory"
	persistencesqlite "github.com/gorenx/goren/session/persistence/sqlite"
	sessionprojection "github.com/gorenx/goren/session/projection"
	"github.com/gorenx/goren/subagent/spawn"
	subagenttool "github.com/gorenx/goren/subagent/tool"
)

type integrationBackgroundWriteFailureSink struct {
	mutex    sync.Mutex
	failures []persistence.BackgroundWriteFailure
}

func newContinuableIntegrationFixture(
	t *testing.T,
	responses [][]llm.StreamChunk,
	extensions ...plugin.Plugin,
) (*integrationFixture, integrationDurability, *integrationAdapter) {
	t.Helper()
	backend := &integrationAdapter{
		responses: responses,
	}
	state, durability := newContinuableIntegrationFixtureWithAdapter(
		t,
		backend,
		extensions...,
	)
	return state, durability, backend
}

func newContinuableIntegrationFixtureWithAdapter(
	t *testing.T,
	backend *integrationAdapter,
	extensions ...plugin.Plugin,
) (*integrationFixture, integrationDurability) {
	t.Helper()
	durability := newIntegrationDurability(t)
	plugins := append([]plugin.Plugin(nil), durability.plugins...)
	plugins = append(plugins, extensions...)
	state := newIntegrationFixtureWithConfiguration(
		t,
		integrationConfiguration{
			agentOptions: agent.Options{
				Provider: "mock",
				Model:    "model",
			},
			plugins: plugins,
			backend: backend,
			delegation: subagenttool.Settings{
				Provider:              spawn.DefaultProviderName,
				ToolName:              subagenttool.DefaultToolName,
				EnableRunInBackground: true,
				BackgroundMode:        subagenttool.BackgroundContinuable,
			},
		},
	)
	return state, durability
}

func waitForContinuableSettlement(
	t *testing.T,
	state *integrationFixture,
	parentHandle agent.Handle,
	waitContext context.Context,
) {
	t.Helper()
	if eventErr := state.lifecycle.waitForEnd(waitContext); eventErr != nil {
		t.Fatal(eventErr)
	}
	if idleErr := parentHandle.Subject.WhenIdle(waitContext); idleErr != nil {
		t.Fatal(idleErr)
	}
}

func (sink *integrationBackgroundWriteFailureSink) ReportBackgroundWriteFailure(
	failure persistence.BackgroundWriteFailure,
) {
	sink.mutex.Lock()
	sink.failures = append(sink.failures, failure)
	sink.mutex.Unlock()
}

func (sink *integrationBackgroundWriteFailureSink) snapshot() []persistence.BackgroundWriteFailure {
	sink.mutex.Lock()
	defer sink.mutex.Unlock()
	return append([]persistence.BackgroundWriteFailure(nil), sink.failures...)
}

type integrationDurability struct {
	plugins  []plugin.Plugin
	sessions *persistence.SessionLogStore
	failures *integrationBackgroundWriteFailureSink
}

func newIntegrationDurability(t *testing.T) integrationDurability {
	t.Helper()
	failures := &integrationBackgroundWriteFailureSink{}
	builder, factoryErr := persistencefactory.New(failures)
	if factoryErr != nil {
		t.Fatal(factoryErr)
	}
	rawConfig, encodeErr := json.Marshal(persistencefactory.Config{
		Path:                 filepath.Join(t.TempDir(), "sessions.sqlite"),
		JournalMode:          persistencesqlite.JournalWAL,
		WriteBatchMaxDelayMS: 1,
	})
	if encodeErr != nil {
		t.Fatal(encodeErr)
	}
	instance, createErr := builder.Create(context.Background(), rawConfig)
	if createErr != nil {
		t.Fatal(createErr)
	}
	sessions, matches := instance.(*persistence.SessionLogStore)
	if !matches {
		t.Fatalf("persistence Factory returned %T", instance)
	}
	return integrationDurability{
		plugins: []plugin.Plugin{
			sessionprojection.NewDriveRegistry(),
			instance,
		},
		sessions: sessions,
		failures: failures,
	}
}
