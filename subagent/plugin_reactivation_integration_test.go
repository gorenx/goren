package subagent_test

import (
	"context"
	"testing"

	"github.com/gorenx/goren/approval"
)

func TestSubagentPluginReactivatesWhenOptionalDependencyAppears(t *testing.T) {
	state := newIntegrationFixture(t, nil)
	settings, err := approval.ValidateConfig(approval.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = state.runtimeEngine.Mount(
		context.Background(),
		approval.New(settings),
	); err != nil {
		t.Fatalf("mount optional DelegationPolicy provider: %v", err)
	}
	if _, err = state.boundDefinitions.List(context.Background()); err != nil {
		t.Fatalf("Bound Definitions after Subagent reactivation: %v", err)
	}
}
