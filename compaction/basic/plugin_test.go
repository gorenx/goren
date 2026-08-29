package basic

import (
	"context"
	"strings"
	"testing"

	"github.com/gorenx/goren/compaction"
)

func TestProviderDeclaresSeparatedArchitecture(t *testing.T) {
	t.Parallel()
	settings, err := ResolveConfig(Config{})
	if err != nil {
		t.Fatal(err)
	}
	owner := New(settings, RuntimeOptions{})
	metadata := owner.Manifest()
	if metadata.Name != PluginName || len(metadata.Provides) != 1 ||
		len(metadata.Requires) != 3 || len(metadata.Optional) != 1 ||
		len(metadata.Waterfalls) != 2 || len(metadata.Events) != 2 {
		t.Fatalf("manifest = %#v", metadata)
	}
	if _, mixed := any(owner).(compaction.Engine); mixed {
		t.Fatal("Plugin must not implement the Compaction business Service")
	}
	if owner.automation.engine != owner.engine {
		t.Fatal("Runtime automation must invoke the published Engine")
	}
	if owner.automation.catalog != owner.catalog ||
		owner.engine.catalog != owner.catalog {
		t.Fatal("Plugin, automation, and Compaction must share one policy catalog")
	}
	_, err = owner.engine.CompactIfNeeded(
		context.Background(),
		compaction.AgentContext{},
		compaction.TriggerPressure,
	)
	if err == nil || !strings.Contains(err.Error(), "Agent Context needs a Session") {
		t.Fatalf("CompactIfNeeded error = %v", err)
	}
}
