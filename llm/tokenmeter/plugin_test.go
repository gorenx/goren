package tokenmeter

import (
	"context"
	"strings"
	"testing"

	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	sessionprojection "github.com/gorenx/goren/session/projection"
)

type tokenMeterFailureReporter struct{}

func (tokenMeterFailureReporter) ReportEventFailure(context.Context, plugin.EventFailure) {}

func (tokenMeterFailureReporter) ReportPostCommitFailure(session.PostCommitFailure) {}

func TestPluginSeparatesLifecycleFromMeterBehavior(t *testing.T) {
	t.Parallel()
	owner := New()
	metadata := owner.Manifest()
	if metadata.Name != PluginName || len(metadata.Provides) != 1 ||
		len(metadata.Optional) != 1 || len(metadata.Events) != 2 {
		t.Fatalf("manifest = %#v", metadata)
	}
	if _, mixed := any(owner).(Meter); mixed {
		t.Fatal("Plugin must not implement the Meter business Service")
	}
	if _, err := owner.implementation.Measure(context.Background(), nil, nil); err == nil ||
		!strings.Contains(err.Error(), "Session is nil") {
		t.Fatalf("Measure error = %v", err)
	}
}

func TestPluginRegistersAndReleasesProjectionUnitsWithItsLifecycle(t *testing.T) {
	t.Parallel()
	reporter := tokenMeterFailureReporter{}
	sessionPlugin, err := session.NewPlugin(session.MemoryStoreOptions{
		PostCommitFailures: reporter,
	})
	if err != nil {
		t.Fatal(err)
	}
	projections := sessionprojection.NewDriveRegistry()
	owner := New()
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{
		EventFailures: reporter,
	})
	handles, err := runtimeEngine.Start(
		context.Background(),
		owner,
		projections,
		sessionPlugin,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if shutdownErr := runtimeEngine.Shutdown(context.Background()); shutdownErr != nil {
			t.Error(shutdownErr)
		}
	})
	conversation := newConversation(t, "plugin-projections")
	active, err := projections.Snapshot(conversation)
	if err != nil {
		t.Fatal(err)
	}
	if len(active.Values) != 3 {
		t.Fatalf("active projections = %#v", active.Values)
	}
	var ownerHandle plugin.Handle
	for _, candidate := range handles {
		status, statusErr := runtimeEngine.Status(candidate)
		if statusErr != nil {
			t.Fatal(statusErr)
		}
		if status.Name == PluginName {
			ownerHandle = candidate
			break
		}
	}
	if ownerHandle.ID() == 0 {
		t.Fatal("Token Meter handle was not returned")
	}
	if err := runtimeEngine.Unload(context.Background(), ownerHandle); err != nil {
		t.Fatal(err)
	}
	released, err := projections.Snapshot(conversation)
	if err != nil {
		t.Fatal(err)
	}
	if len(released.Values) != 0 {
		t.Fatalf("released projections = %#v", released.Values)
	}
}
