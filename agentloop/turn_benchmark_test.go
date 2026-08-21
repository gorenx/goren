package agentloop

import (
	"context"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/systemprompt"
	"github.com/gorenx/goren/tools"
)

type turnBenchmarkEventFailures struct{}

func (turnBenchmarkEventFailures) ReportEventFailure(
	context.Context,
	plugin.EventFailure,
) {
}

type turnBenchmarkPostCommitFailures struct{}

func (turnBenchmarkPostCommitFailures) ReportPostCommitFailure(
	session.PostCommitFailure,
) {
}

type turnBenchmarkHarness struct {
	runtimeEngine *plugin.Runtime
	handleState   agent.Handle
	runner        *turnRunner
}

func newTurnBenchmarkHarness(
	benchmarkState *testing.B,
) *turnBenchmarkHarness {
	benchmarkState.Helper()
	promptSettings, err := systemprompt.ValidateConfig(systemprompt.Config{})
	if err != nil {
		benchmarkState.Fatal(err)
	}
	toolSettings, err := tools.ValidateConfig(tools.Config{})
	if err != nil {
		benchmarkState.Fatal(err)
	}
	loopPlugin, err := New(
		Settings{
			MaxParallelToolCalls: DefaultMaxParallelToolCalls,
		},
		RuntimeOptions{},
	)
	if err != nil {
		benchmarkState.Fatal(err)
	}
	sessions, err := session.NewMemoryStore(session.MemoryStoreOptions{
		PostCommitFailures: turnBenchmarkPostCommitFailures{},
	})
	if err != nil {
		benchmarkState.Fatal(err)
	}
	agents := agent.NewRegistry(agent.RegistryOptions{})
	models := llm.NewRuntime(nil)
	prompts := systemprompt.New(
		promptSettings,
		systemprompt.RegistryOptions{},
	)
	toolService := tools.New(toolSettings)
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{
		EventFailures: turnBenchmarkEventFailures{},
	})
	if _, err = runtimeEngine.Start(
		context.Background(),
		agents,
		sessions,
		models,
		prompts,
		toolService,
		loopPlugin,
	); err != nil {
		benchmarkState.Fatal(err)
	}
	handleState, err := agents.Create(
		context.Background(),
		agent.CreateOptions{
			SessionID: "turn-benchmark",
			AgentOptions: agent.Options{
				Provider: "benchmark",
				Model:    "model",
			},
		},
	)
	if err != nil {
		benchmarkState.Fatal(err)
	}
	subject, matches := handleState.Subject.(*ReactLoopAgent)
	if !matches {
		benchmarkState.Fatal("benchmark Agent is not ReactLoopAgent")
	}
	state := &turnBenchmarkHarness{
		runtimeEngine: runtimeEngine,
		handleState:   handleState,
		runner:        subject.loop.turns,
	}
	benchmarkState.Cleanup(func() {
		if disposeErr := state.handleState.Dispose(context.Background()); disposeErr != nil {
			benchmarkState.Error(disposeErr)
		}
		if shutdownErr := state.runtimeEngine.Shutdown(context.Background()); shutdownErr != nil {
			benchmarkState.Error(shutdownErr)
		}
	})
	return state
}

func BenchmarkTurnPrepareEmptyStep(b *testing.B) {
	state := newTurnBenchmarkHarness(b)
	b.ReportAllocs()
	for b.Loop() {
		prepared, err := state.runner.prepareStep(
			context.Background(),
			agent.NextStep,
			1,
			1,
		)
		if err != nil {
			b.Fatal(err)
		}
		if prepared.rejected || len(prepared.messages) != 0 {
			b.Fatal("empty step preparation changed")
		}
	}
}

func BenchmarkTurnRunEmpty(b *testing.B) {
	state := newTurnBenchmarkHarness(b)
	b.ReportAllocs()
	for b.Loop() {
		continued, err := state.runner.runTurn(context.Background())
		if err != nil {
			b.Fatal(err)
		}
		if continued {
			b.Fatal("empty Turn unexpectedly continued")
		}
	}
}
