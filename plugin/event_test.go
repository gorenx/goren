package plugin_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/gorenx/goren/plugin"
)

type advanced struct {
	Value int
}

func (advanced) EventName() string {
	return "counter/advanced"
}

func (advanced) EventDelivery() plugin.DeliveryPolicy {
	return plugin.DeliveryOrdered
}

type eventObserverPlugin struct {
	plugin.Base
	name  string
	order *[]string
}

func (observer *eventObserverPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: observer.name,
		Events: []plugin.EventSubscription{
			plugin.EventOf[advanced](),
		},
	}
}

func (*eventObserverPlugin) Apply(context.Context) error {
	return nil
}

func (*eventObserverPlugin) Dispose(context.Context) error {
	return nil
}

func (observer *eventObserverPlugin) ObserveEvent(
	_ context.Context,
	_ advanced,
) error {
	*observer.order = append(*observer.order, observer.name)
	return nil
}

type eventPublisherPlugin struct {
	plugin.Base
	name string
}

func (publisher *eventPublisherPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: publisher.name,
	}
}

func (*eventPublisherPlugin) Apply(context.Context) error {
	return nil
}

func (*eventPublisherPlugin) Dispose(context.Context) error {
	return nil
}

func TestEventRoutesFromCurrentScopeToRoot(t *testing.T) {
	t.Parallel()
	order := make([]string, 0)
	rootObserver := &eventObserverPlugin{
		name:  "root-observer",
		order: &order,
	}
	rootPublisher := &eventPublisherPlugin{
		name: "root-publisher",
	}
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	handles, err := runtimeEngine.Start(
		context.Background(),
		rootObserver,
		rootPublisher,
	)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	childObserver := &eventObserverPlugin{
		name:  "child-observer",
		order: &order,
	}
	childObserverHandle, err := runtimeEngine.MountChild(
		context.Background(),
		handles[1],
		childObserver,
	)
	if err != nil {
		t.Fatalf("mount child observer: %v", err)
	}
	childPublisher := &eventPublisherPlugin{
		name: "child-publisher",
	}
	if _, err := runtimeEngine.MountChild(
		context.Background(),
		childObserverHandle,
		childPublisher,
	); err != nil {
		t.Fatalf("mount child publisher: %v", err)
	}
	if err := plugin.Publish(
		context.Background(),
		childPublisher,
		advanced{
			Value: 1,
		},
	); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if got := strings.Join(order, ","); got != "child-observer,root-observer" {
		t.Fatalf("observer order = %q", got)
	}
}

type bestEffortFact struct{}

func (bestEffortFact) EventName() string {
	return "best-effort"
}

func (bestEffortFact) EventDelivery() plugin.DeliveryPolicy {
	return plugin.DeliveryBestEffort
}

type failingObserver struct {
	plugin.Base
}

func (*failingObserver) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "failing-observer",
		Events: []plugin.EventSubscription{
			plugin.EventOf[bestEffortFact](),
		},
	}
}

func (*failingObserver) Apply(context.Context) error {
	return nil
}

func (*failingObserver) Dispose(context.Context) error {
	return nil
}

func (*failingObserver) ObserveEvent(context.Context, bestEffortFact) error {
	return errors.New("observer failed")
}

type failureReporter struct {
	mutex    sync.Mutex
	failures []plugin.EventFailure
}

func (reporter *failureReporter) ReportEventFailure(
	_ context.Context,
	failure plugin.EventFailure,
) {
	reporter.mutex.Lock()
	reporter.failures = append(reporter.failures, failure)
	reporter.mutex.Unlock()
}

func TestBestEffortEventReportsAndSuppressesObserverFailure(t *testing.T) {
	t.Parallel()
	reporter := &failureReporter{}
	observer := &failingObserver{}
	publisher := &eventPublisherPlugin{
		name: "best-effort-publisher",
	}
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{
		EventFailures: reporter,
	})
	if _, err := runtimeEngine.Start(
		context.Background(),
		observer,
		publisher,
	); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := plugin.Publish(
		context.Background(),
		publisher,
		bestEffortFact{},
	); err != nil {
		t.Fatalf("publish: %v", err)
	}
	reporter.mutex.Lock()
	defer reporter.mutex.Unlock()
	if len(reporter.failures) != 1 {
		t.Fatalf("reported failures = %d, want 1", len(reporter.failures))
	}
}

func TestBestEffortObserverRequiresFailureReporter(t *testing.T) {
	t.Parallel()
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	if _, err := runtimeEngine.Start(
		context.Background(),
		&failingObserver{},
	); err == nil || !strings.Contains(err.Error(), "EventFailureReporter") {
		t.Fatalf("Start error = %v", err)
	}
}
