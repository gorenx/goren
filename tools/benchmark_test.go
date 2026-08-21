package tools_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/systemprompt"
	"github.com/gorenx/goren/tools"
)

var (
	benchmarkDefinition tools.ToolDefinition
	benchmarkSchemas    []llm.ToolSchema
	benchmarkMode       tools.ToolExecutionMode
	benchmarkResult     tools.ToolExecutionResult
)

type benchmarkFailureReporter struct{}

func (benchmarkFailureReporter) ReportEventFailure(
	context.Context,
	plugin.EventFailure,
) {
}

func newToolsBenchmark(
	benchmarkState *testing.B,
	toolCount int,
) *tools.Service {
	benchmarkState.Helper()
	promptSettings, err := systemprompt.ValidateConfig(systemprompt.Config{})
	if err != nil {
		benchmarkState.Fatal(err)
	}
	toolSettings, err := tools.ValidateConfig(tools.Config{})
	if err != nil {
		benchmarkState.Fatal(err)
	}
	prompts := systemprompt.New(
		promptSettings,
		systemprompt.RegistryOptions{},
	)
	service := tools.New(toolSettings)
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{
		EventFailures: benchmarkFailureReporter{},
	})
	if _, err = runtimeEngine.Start(
		context.Background(),
		prompts,
		service,
	); err != nil {
		benchmarkState.Fatal(err)
	}
	benchmarkState.Cleanup(func() {
		if shutdownErr := runtimeEngine.Shutdown(context.Background()); shutdownErr != nil {
			benchmarkState.Error(shutdownErr)
		}
	})
	for toolIndex := 0; toolIndex < toolCount; toolIndex++ {
		definition := objectTool(
			fmt.Sprintf("tool-%d", toolIndex),
			"benchmark tool",
			tools.ExecutorFunc(passThroughBody),
		)
		definition.ConcurrencyBehavior = tools.ConcurrencyClassifierFunc(func(
			json.RawMessage,
		) bool {
			return true
		})
		if _, err = service.AddTool(context.Background(), definition); err != nil {
			benchmarkState.Fatal(err)
		}
	}
	return service
}

func BenchmarkToolRegistryGet(b *testing.B) {
	for _, toolCount := range []int{1, 32, 256} {
		b.Run(fmt.Sprintf("tools=%d", toolCount), func(b *testing.B) {
			service := newToolsBenchmark(b, toolCount)
			selectedName := fmt.Sprintf("tool-%d", toolCount-1)
			b.ReportAllocs()
			b.ResetTimer()
			for benchmarkIndex := 0; benchmarkIndex < b.N; benchmarkIndex++ {
				definition, found := service.Get(selectedName)
				if !found {
					b.Fatal("tool not found")
				}
				benchmarkDefinition = definition
			}
		})
	}
}

func BenchmarkToolRegistrySchemas(b *testing.B) {
	for _, toolCount := range []int{1, 32, 256} {
		b.Run(fmt.Sprintf("tools=%d", toolCount), func(b *testing.B) {
			service := newToolsBenchmark(b, toolCount)
			b.ReportAllocs()
			b.ResetTimer()
			for benchmarkIndex := 0; benchmarkIndex < b.N; benchmarkIndex++ {
				benchmarkSchemas = service.Schemas()
			}
		})
	}
}

func BenchmarkToolExecutionMode(b *testing.B) {
	for _, toolCount := range []int{1, 32, 256} {
		b.Run(fmt.Sprintf("tools=%d", toolCount), func(b *testing.B) {
			service := newToolsBenchmark(b, toolCount)
			input := tools.ToolExecutionInput{
				CallID:    "benchmark-call",
				Name:      fmt.Sprintf("tool-%d", toolCount-1),
				Arguments: json.RawMessage(`{"value":true}`),
			}
			b.ReportAllocs()
			b.ResetTimer()
			for benchmarkIndex := 0; benchmarkIndex < b.N; benchmarkIndex++ {
				benchmarkMode = service.ExecutionMode(input)
			}
		})
	}
}

func BenchmarkToolExecute(b *testing.B) {
	for _, toolCount := range []int{1, 32, 256} {
		b.Run(fmt.Sprintf("tools=%d", toolCount), func(b *testing.B) {
			service := newToolsBenchmark(b, toolCount)
			input := tools.ToolExecutionInput{
				CallID:    "benchmark-call",
				Name:      fmt.Sprintf("tool-%d", toolCount-1),
				Arguments: json.RawMessage(`{"value":true}`),
			}
			b.ReportAllocs()
			b.ResetTimer()
			for benchmarkIndex := 0; benchmarkIndex < b.N; benchmarkIndex++ {
				outcome := service.Execute(context.Background(), input)
				if outcome.Failed() {
					b.Fatal(outcome.FailureDetail())
				}
				benchmarkResult = outcome
			}
		})
	}
}
