package agentloop_test

import (
	"context"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentloop"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/systemprompt"
	"github.com/gorenx/goren/tools"
)

type benchmarkAdapter struct {
	response []llm.StreamChunk
}

func (backend *benchmarkAdapter) Stream(
	context.Context,
	llm.GenerateOptions,
) (llm.ChunkStream, error) {
	return llm.NewSliceStream(backend.response)
}

type benchmarkHarness struct {
	runtimeEngine *plugin.Runtime
	agents        *agent.RegistryPlugin
	adapter       llm.AdapterRegistrationHandle
}

func (state *benchmarkHarness) createAgent(
	benchmarkState *testing.B,
) agent.Handle {
	benchmarkState.Helper()
	handleState, err := state.agents.Create(
		context.Background(),
		agent.CreateOptions{
			SessionID: "benchmark-turn",
			AgentOptions: agent.Options{
				Provider: "benchmark",
				Model:    "model",
			},
		},
	)
	if err != nil {
		benchmarkState.Fatal(err)
	}
	return handleState
}

func newAgentLoopBenchmark(
	benchmarkState *testing.B,
	chunkCount int,
) *benchmarkHarness {
	benchmarkState.Helper()
	promptSettings, err := systemprompt.ValidateConfig(systemprompt.Config{})
	if err != nil {
		benchmarkState.Fatal(err)
	}
	toolSettings, err := tools.ValidateConfig(tools.Config{})
	if err != nil {
		benchmarkState.Fatal(err)
	}
	loopPlugin, err := agentloop.New(
		agentloop.Settings{
			MaxParallelToolCalls: agentloop.DefaultMaxParallelToolCalls,
		},
		agentloop.RuntimeOptions{},
	)
	if err != nil {
		benchmarkState.Fatal(err)
	}
	sessions, err := session.NewMemoryStore(session.MemoryStoreOptions{
		PostCommitFailures: postCommitFailureSink{},
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
		EventFailures: eventFailureSink{},
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
	response := make([]llm.StreamChunk, 0, chunkCount+1)
	for chunkIndex := 0; chunkIndex < chunkCount; chunkIndex++ {
		response = append(response, llm.TextDeltaChunk{
			Index: 0,
			Text:  "x",
		})
	}
	response = append(response, llm.FinishChunk{
		Reason: llm.StopFinish{},
	})
	adapterHandle, err := models.RegisterAdapter(
		context.Background(),
		[]string{
			"benchmark",
		},
		&benchmarkAdapter{
			response: response,
		},
	)
	if err != nil {
		benchmarkState.Fatal(err)
	}
	state := &benchmarkHarness{
		runtimeEngine: runtimeEngine,
		agents:        agents,
		adapter:       adapterHandle,
	}
	benchmarkState.Cleanup(func() {
		if releaseErr := state.adapter.Release(context.Background()); releaseErr != nil {
			benchmarkState.Error(releaseErr)
		}
		if shutdownErr := state.runtimeEngine.Shutdown(context.Background()); shutdownErr != nil {
			benchmarkState.Error(shutdownErr)
		}
	})
	return state
}

func BenchmarkAgentTurnLifecycle(b *testing.B) {
	for _, chunkCount := range []int{1, 16, 64} {
		b.Run(benchmarkChunkLabel(chunkCount), func(b *testing.B) {
			state := newAgentLoopBenchmark(b, chunkCount)
			message := userMessageValue("benchmark input")
			b.ReportAllocs()
			b.ResetTimer()
			for benchmarkIndex := 0; benchmarkIndex < b.N; benchmarkIndex++ {
				handleState := state.createAgent(b)
				var err error
				if err = handleState.Subject.Followup(message); err != nil {
					b.Fatal(err)
				}
				if err = handleState.Subject.WhenIdle(context.Background()); err != nil {
					b.Fatal(err)
				}
				if err = handleState.Dispose(context.Background()); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkAgentTurnRun excludes Agent tree mount and unmount so changes in
// turn.go can be measured independently from Plugin topology construction.
func BenchmarkAgentTurnRun(b *testing.B) {
	for _, chunkCount := range []int{1, 16, 64} {
		b.Run(benchmarkChunkLabel(chunkCount), func(b *testing.B) {
			state := newAgentLoopBenchmark(b, chunkCount)
			message := userMessageValue("benchmark input")
			b.ReportAllocs()
			b.ResetTimer()
			for benchmarkIndex := 0; benchmarkIndex < b.N; benchmarkIndex++ {
				b.StopTimer()
				handleState := state.createAgent(b)
				b.StartTimer()
				if err := handleState.Subject.Followup(message); err != nil {
					b.Fatal(err)
				}
				if err := handleState.Subject.WhenIdle(context.Background()); err != nil {
					b.Fatal(err)
				}
				b.StopTimer()
				if err := handleState.Dispose(context.Background()); err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
			}
		})
	}
}

func benchmarkChunkLabel(chunkCount int) string {
	switch chunkCount {
	case 1:
		return "chunks=1"
	case 16:
		return "chunks=16"
	case 64:
		return "chunks=64"
	default:
		panic("unsupported benchmark chunk count")
	}
}
