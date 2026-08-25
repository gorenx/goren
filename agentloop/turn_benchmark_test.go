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

type benchmarkScopeRuntime struct{}

func (benchmarkScopeRuntime) Dispatch(context.Context, agent.RuntimeEvent) error {
	return nil
}

func (benchmarkScopeRuntime) ResolvePreStep(
	requestContext context.Context,
	notice agent.PreStepNotice,
	terminal agent.PreStepAction,
) (agent.PreStepDecision, error) {
	return terminal.Execute(requestContext, notice)
}

func (benchmarkScopeRuntime) ResolveRequest(
	requestContext context.Context,
	notice agent.RequestNotice,
	terminal agent.RequestAction,
) (agent.RequestResolution, error) {
	return terminal.Execute(requestContext, notice)
}

func (benchmarkScopeRuntime) ResolveRequestError(
	requestContext context.Context,
	notice agent.RequestErrorNotice,
	terminal agent.RequestErrorHandler,
) (agent.RequestErrorAction, error) {
	return terminal.Execute(requestContext, notice)
}

func (benchmarkScopeRuntime) Provision(context.Context, agent.Provisioner) error {
	return nil
}

func (benchmarkScopeRuntime) Teardown(context.Context) error { return nil }

type turnBenchmarkHarness struct {
	runtimeEngine *plugin.Runtime
	handleState   agent.Handle
	runner        *turnRunner
}

var benchmarkConstructedAgent *ReactLoopAgent

func BenchmarkAgentConstruct(b *testing.B) {
	conversation, err := session.New(
		"agent-construction-benchmark",
		session.CreateOptions{},
	)
	if err != nil {
		b.Fatal(err)
	}
	loopOptions := agent.Options{
		Provider: "benchmark",
		Model:    "model",
	}
	failures := newObserverFailureReporter(nil)
	b.ReportAllocs()
	for b.Loop() {
		benchmarkConstructedAgent, err = newReactLoopAgent(
			conversation,
			loopOptions,
			DefaultMaxParallelToolCalls,
			failures,
			benchmarkScopeRuntime{},
		)
		if err != nil {
			b.Fatal(err)
		}
	}
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
	sessionPlugin, err := session.NewPlugin(session.MemoryStoreOptions{
		PostCommitFailures: turnBenchmarkPostCommitFailures{},
	})
	if err != nil {
		benchmarkState.Fatal(err)
	}
	agents := agent.NewRegistry(agent.RegistryOptions{})
	agentPlugin, err := agent.NewRegistryPlugin(agents)
	if err != nil {
		benchmarkState.Fatal(err)
	}
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
		agentPlugin,
		sessionPlugin,
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
		plan, err := state.runner.executor.prepare(
			context.Background(),
			agent.NextStep,
			1,
			1,
		)
		if err != nil {
			b.Fatal(err)
		}
		if plan.rejected || plan.hasMessages() {
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
