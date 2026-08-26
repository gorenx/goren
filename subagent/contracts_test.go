package subagent_test

import (
	"testing"

	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
	subagentruntime "github.com/gorenx/goren/subagent/runtime"
)

func TestStartCommandsKeepImplementationInputsSeparate(t *testing.T) {
	t.Parallel()
	label := "terminal"
	oneShot, err := subagent.NewOneShotStart(
		subagent.ChildRequest{},
		subagent.OneShotOptions{
			SeedBuilder: "spawn",
			Label:       &label,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	childID := session.SessionID("durable-child")
	continuable, err := subagent.NewContinuableStart(
		subagent.ChildRequest{},
		subagent.ContinuableOptions{
			SeedBuilder: "fork",
			Label:       "background",
			ChildID:     &childID,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if oneShot.Mode() != subagent.ModeOneShot ||
		oneShot.RequestedChildID() != nil {
		t.Fatalf("OneShot command = %#v", oneShot)
	}
	if continuable.Mode() != subagent.ModeContinuable ||
		continuable.RequestedChildID() == nil ||
		*continuable.RequestedChildID() != childID {
		t.Fatalf("Continuable command = %#v", continuable)
	}
	label = "changed"
	childID = "changed"
	if oneShot.Label() == nil || *oneShot.Label() != "terminal" ||
		*continuable.RequestedChildID() != "durable-child" {
		t.Fatal("StartCommand retained caller-owned pointers")
	}
}

func TestDescriptorAndLifecycleVocabulary(t *testing.T) {
	t.Parallel()
	if !session.IsKnownEventType(subagent.DescriptorEventName) {
		t.Fatal("descriptor event type is not registered")
	}
	if (subagent.SeedBuilderAdded{}).EventDelivery() != plugin.DeliveryOrdered {
		t.Fatal("SeedBuilderAdded must remain vetoable")
	}
	if (subagent.SeedBuilderRemoved{}).EventDelivery() != plugin.DeliveryBestEffort {
		t.Fatal("SeedBuilderRemoved must contain observer failures")
	}
	if (subagent.Started{}).EventName() != "subagent/start" ||
		(subagent.Ended{}).EventName() != "subagent/end" {
		t.Fatal("execution lifecycle event names drifted")
	}
}

func TestRuntimeProvidesOnlyPublicBusinessCapabilities(t *testing.T) {
	t.Parallel()
	providedNames := map[string]bool{}
	for _, capabilityType := range subagentruntime.New(
		subagentruntime.RuntimeOptions{},
	).Manifest().Provides {
		providedNames[capabilityType.Name()] = true
	}
	wantedTypes := []plugin.ServiceType{
		plugin.ServiceOf[subagent.SeedBuilderRegistry](),
		plugin.ServiceOf[subagent.Starter](),
		plugin.ServiceOf[subagent.ChildControl](),
		plugin.ServiceOf[subagent.ParentReporter](),
		plugin.ServiceOf[subagent.ExtensionRegistry](),
		plugin.ServiceOf[subagent.ChildDirectory](),
	}
	for _, wantedType := range wantedTypes {
		if !providedNames[wantedType.Name()] {
			t.Fatalf("Runtime does not provide %q", wantedType.Name())
		}
	}
}
