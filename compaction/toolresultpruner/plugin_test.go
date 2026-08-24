package toolresultpruner

import (
	"context"
	"testing"

	"github.com/gorenx/goren/llm/tokenmeter"
	"github.com/gorenx/goren/plugin"
)

type prunerFailureReporter struct{}

func (prunerFailureReporter) ReportEventFailure(context.Context, plugin.EventFailure) {}

func TestPluginSeparatesLifecycleFromPrunerBehavior(t *testing.T) {
	t.Parallel()
	settings, err := ResolveConfig(Config{})
	if err != nil {
		t.Fatal(err)
	}
	owner := New(settings)
	metadata := owner.Manifest()
	if metadata.Name != PluginName || len(metadata.Provides) != 1 ||
		len(metadata.Requires) != 1 {
		t.Fatalf("manifest = %#v", metadata)
	}
	if _, mixed := any(owner).(Pruner); mixed {
		t.Fatal("Plugin must not implement the Pruner business Service")
	}
	if measured, err := owner.implementation.MeasureContent(nil); err != nil || measured != 0 {
		t.Fatalf("MeasureContent = %d, %v", measured, err)
	}
}

func TestPluginBindsAndReleasesMeterWithRuntimeLifecycle(t *testing.T) {
	t.Parallel()
	settings, err := ResolveConfig(Config{})
	if err != nil {
		t.Fatal(err)
	}
	owner := New(settings)
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{
		EventFailures: prunerFailureReporter{},
	})
	if _, err := runtimeEngine.Start(
		context.Background(),
		owner,
		tokenmeter.New(),
	); err != nil {
		t.Fatal(err)
	}
	if owner.implementation.meter == nil {
		t.Fatal("Apply did not bind Token Meter")
	}
	if err := runtimeEngine.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if owner.implementation.meter != nil {
		t.Fatal("Dispose retained Token Meter")
	}
}
