package llm_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
)

type topologyObserverPlugin struct {
	plugin.Base
	failure error
	count   int
}

func (*topologyObserverPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "llm-topology-observer",
		Events: []plugin.EventSubscription{
			plugin.EventOf[llm.AdaptersUpdated](),
		},
	}
}

func (*topologyObserverPlugin) Apply(context.Context) error {
	return nil
}

func (*topologyObserverPlugin) Dispose(context.Context) error {
	return nil
}

func (observer *topologyObserverPlugin) ObserveEvent(
	requestContext context.Context,
	fact plugin.Event,
) error {
	if err := requestContext.Err(); err != nil {
		return err
	}
	if _, matches := fact.(llm.AdaptersUpdated); !matches {
		return errors.New("LLM topology observer received an unsupported Event")
	}
	observer.count++
	return observer.failure
}

type failureReporter struct {
	mu       sync.Mutex
	failures []error
}

func (reporter *failureReporter) ReportObserverFailure(failure error) {
	reporter.mu.Lock()
	reporter.failures = append(reporter.failures, failure)
	reporter.mu.Unlock()
}

func (reporter *failureReporter) snapshot() []error {
	reporter.mu.Lock()
	defer reporter.mu.Unlock()
	return append([]error(nil), reporter.failures...)
}

func TestAdapterTopologyPublishesCommittedUpdates(t *testing.T) {
	t.Parallel()
	reporter := &failureReporter{}
	serviceValue := llm.NewRuntime(reporter)
	observer := &topologyObserverPlugin{
		failure: errors.New("contained observer failure"),
	}
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	if _, err := runtimeEngine.Start(context.Background(), serviceValue, observer); err != nil {
		t.Fatal(err)
	}
	defer runtimeEngine.Shutdown(context.Background())

	pluginHandle, _ := loadAdapter(
		t,
		runtimeEngine,
		"event-adapter",
		[]string{"event-route"},
		&fakeAdapter{},
	)
	if observer.count != 1 {
		t.Fatalf("topology update count = %d, want 1", observer.count)
	}
	if failures := reporter.snapshot(); len(failures) != 1 || failures[0].Error() != "contained observer failure" {
		t.Fatalf("reported failures = %#v", failures)
	}
	if err := runtimeEngine.Unload(context.Background(), pluginHandle); err != nil {
		t.Fatal(err)
	}
	if observer.count != 2 {
		t.Fatalf("topology update count after unload = %d, want 2", observer.count)
	}
}

func TestInvariantTopologyFailureRollsBackAdapterRegistration(t *testing.T) {
	t.Parallel()
	serviceValue := llm.NewRuntime(nil)
	observer := &topologyObserverPlugin{
		failure: llm.MustLlmError("invalid topology", "INVARIANT"),
	}
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	if _, err := runtimeEngine.Start(context.Background(), serviceValue, observer); err != nil {
		t.Fatal(err)
	}
	defer runtimeEngine.Shutdown(context.Background())

	_, err := runtimeEngine.Mount(
		context.Background(),
		&adapterOwnerPlugin{
			pluginName: "invalid-event-adapter",
			routes:     []string{"invalid-event-route"},
			backend:    &fakeAdapter{},
		},
	)
	if llmErrorCode(err) != "INVARIANT" {
		t.Fatalf("Mount error = %v", err)
	}
	if providers := serviceValue.ListProviders(); len(providers) != 0 {
		t.Fatalf("providers after rejected update = %#v", providers)
	}
}
