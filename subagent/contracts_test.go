package subagent_test

import (
	"context"
	"errors"
	"testing"

	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
	subagentruntime "github.com/gorenx/goren/subagent/runtime"
)

type oneShotProvider struct{}

func (oneShotProvider) Name() string {
	return "one-shot"
}

func (oneShotProvider) Capabilities() subagent.Capabilities {
	return subagent.Capabilities{}
}

func (oneShotProvider) InheritsParentContext() bool {
	return false
}

func (oneShotProvider) Start(
	context.Context,
	subagent.ResolvedStartRequest,
) (subagent.Run, error) {
	return nil, errors.New("fixture")
}

type continuableProvider struct {
	oneShotProvider
}

func (continuableProvider) PrepareContinuable(
	context.Context,
	subagent.ContinuableCreateRequest,
) (subagent.ContinuableCreateSpec, error) {
	return subagent.ContinuableCreateSpec{}, nil
}

func TestContinuableIsAnAdditionalProviderCapability(t *testing.T) {
	t.Parallel()
	var baseProvider subagent.Provider = oneShotProvider{}
	if _, supported := baseProvider.(subagent.ContinuableProvider); supported {
		t.Fatal("base one-shot Provider unexpectedly supports continuable creation")
	}
	var extendedProvider subagent.Provider = continuableProvider{}
	if _, supported := extendedProvider.(subagent.ContinuableProvider); !supported {
		t.Fatal("continuable Provider lost its base one-shot contract")
	}
}

func TestRuntimeRejectsUnknownOneShotProvider(t *testing.T) {
	state := newRuntimeFixture(t, false)
	_, err := state.services.oneShots.Start(
		context.Background(),
		"one-shot",
		subagent.StartRequest{},
	)
	var problem *subagent.Error
	if !errors.As(err, &problem) || problem.Code != subagent.ErrorNoProvider {
		t.Fatalf("Start error = %v, want NO_PROVIDER", err)
	}
}

func TestDescriptorAndLifecycleVocabulary(t *testing.T) {
	t.Parallel()
	if !session.IsKnownEventType(subagent.DescriptorEventName) {
		t.Fatal("descriptor event type is not registered")
	}
	if (subagent.ProviderAdded{}).EventDelivery() != plugin.DeliveryOrdered {
		t.Fatal("ProviderAdded must remain vetoable")
	}
	if (subagent.ProviderRemoved{}).EventDelivery() != plugin.DeliveryBestEffort {
		t.Fatal("ProviderRemoved must contain observer failures")
	}
	if (subagent.Started{}).EventName() != "subagent/start" ||
		(subagent.Ended{}).EventName() != "subagent/end" {
		t.Fatal("run lifecycle event names drifted")
	}
}

func TestRuntimeProvidesOnlyImplementedCapabilityInterfaces(t *testing.T) {
	t.Parallel()
	providedNames := map[string]bool{}
	for _, capabilityType := range subagentruntime.New().Manifest().Provides {
		providedNames[capabilityType.Name()] = true
	}
	wantedNames := []string{
		plugin.ServiceOf[subagent.ProviderRegistry]().Name(),
		plugin.ServiceOf[subagent.OneShotService]().Name(),
		plugin.ServiceOf[subagent.ContinuableService]().Name(),
		plugin.ServiceOf[subagent.SetupRegistry]().Name(),
	}
	for _, wantedName := range wantedNames {
		if !providedNames[wantedName] {
			t.Fatalf("Runtime does not provide %q", wantedName)
		}
	}
	deferredNames := []string{
		plugin.ServiceOf[subagent.Catalog]().Name(),
	}
	for _, deferredName := range deferredNames {
		if providedNames[deferredName] {
			t.Fatalf("Runtime advertises unfinished capability %q", deferredName)
		}
	}
}

var _ subagent.Provider = oneShotProvider{}
var _ subagent.ContinuableProvider = continuableProvider{}
