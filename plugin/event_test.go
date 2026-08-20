package plugin_test

import (
	"context"
	"errors"
	"fmt"
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
	requestContext context.Context,
	fact plugin.Event,
) error {
	if err := requestContext.Err(); err != nil {
		return err
	}
	if _, matches := fact.(advanced); !matches {
		return errors.New("event observer received an undeclared Event")
	}
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

type reset struct {
	Reason string
}

func (reset) EventName() string {
	return "counter/reset"
}

func (reset) EventDelivery() plugin.DeliveryPolicy {
	return plugin.DeliveryOrdered
}

type ignored struct{}

func (ignored) EventName() string {
	return "counter/ignored"
}

func (ignored) EventDelivery() plugin.DeliveryPolicy {
	return plugin.DeliveryOrdered
}

type multiEventObserverPlugin struct {
	plugin.Base
	events    []string
	duplicate bool
}

func (observer *multiEventObserverPlugin) Manifest() plugin.Manifest {
	subscriptions := []plugin.EventSubscription{
		plugin.EventOf[advanced](),
		plugin.EventOf[reset](),
	}
	if observer.duplicate {
		subscriptions = append(subscriptions, plugin.EventOf[advanced]())
	}
	return plugin.Manifest{
		Name:   "multi-event-observer",
		Events: subscriptions,
	}
}

func (*multiEventObserverPlugin) Apply(context.Context) error {
	return nil
}

func (*multiEventObserverPlugin) Dispose(context.Context) error {
	return nil
}

func (observer *multiEventObserverPlugin) ObserveEvent(
	requestContext context.Context,
	fact plugin.Event,
) error {
	if err := requestContext.Err(); err != nil {
		return err
	}
	switch typedFact := fact.(type) {
	case advanced:
		observer.events = append(observer.events, fmt.Sprintf("advanced:%d", typedFact.Value))
		return nil
	case reset:
		observer.events = append(observer.events, "reset:"+typedFact.Reason)
		return nil
	default:
		return fmt.Errorf("unsupported Event %q", fact.EventName())
	}
}

func TestPluginObservesMultipleDeclaredEventTypesThroughOneEntryPoint(t *testing.T) {
	t.Parallel()
	observer := &multiEventObserverPlugin{}
	publisher := &eventPublisherPlugin{
		name: "multi-event-publisher",
	}
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	handles, err := runtimeEngine.Start(context.Background(), observer, publisher)
	if err != nil {
		t.Fatal(err)
	}
	if err := plugin.Publish(
		context.Background(),
		publisher,
		advanced{
			Value: 7,
		},
	); err != nil {
		t.Fatal(err)
	}
	if err := plugin.Publish(
		context.Background(),
		publisher,
		reset{
			Reason: "manual",
		},
	); err != nil {
		t.Fatal(err)
	}
	if err := plugin.Publish(
		context.Background(),
		publisher,
		ignored{},
	); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(observer.events, ","); got != "advanced:7,reset:manual" {
		t.Fatalf("observed Events = %q", got)
	}
	if err := runtimeEngine.Unload(context.Background(), handles[0]); err != nil {
		t.Fatal(err)
	}
	if err := plugin.Publish(
		context.Background(),
		publisher,
		advanced{
			Value: 8,
		},
	); err != nil {
		t.Fatal(err)
	}
	if err := plugin.Publish(
		context.Background(),
		publisher,
		reset{
			Reason: "after-unload",
		},
	); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(observer.events, ","); got != "advanced:7,reset:manual" {
		t.Fatalf("observed Events after unload = %q", got)
	}
}

func TestPluginRejectsDuplicateEventTypeDeclaration(t *testing.T) {
	t.Parallel()
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	if _, err := runtimeEngine.Start(
		context.Background(),
		&multiEventObserverPlugin{
			duplicate: true,
		},
	); err == nil || !strings.Contains(err.Error(), "more than once") {
		t.Fatalf("Start error = %v", err)
	}
}

type eventDeclarationOnlyPlugin struct {
	plugin.Base
}

func (*eventDeclarationOnlyPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "event-declaration-only",
		Events: []plugin.EventSubscription{
			plugin.EventOf[advanced](),
		},
	}
}

func (*eventDeclarationOnlyPlugin) Apply(context.Context) error {
	return nil
}

func (*eventDeclarationOnlyPlugin) Dispose(context.Context) error {
	return nil
}

func TestPluginDeclaringEventsMustImplementUnifiedObserver(t *testing.T) {
	t.Parallel()
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	if _, err := runtimeEngine.Start(
		context.Background(),
		&eventDeclarationOnlyPlugin{},
	); err == nil || !strings.Contains(err.Error(), "does not implement EventObserver") {
		t.Fatalf("Start error = %v", err)
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

func (observer *failingObserver) Manifest() plugin.Manifest {
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

func (*failingObserver) ObserveEvent(context.Context, plugin.Event) error {
	return errors.New("observer failed")
}

type panickingObserver struct {
	plugin.Base
}

func (*panickingObserver) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "panicking-observer",
		Events: []plugin.EventSubscription{
			plugin.EventOf[bestEffortFact](),
		},
	}
}

func (*panickingObserver) Apply(context.Context) error {
	return nil
}

func (*panickingObserver) Dispose(context.Context) error {
	return nil
}

func (*panickingObserver) ObserveEvent(context.Context, plugin.Event) error {
	panic("observer panic")
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

func TestBestEffortEventContainsAndReportsObserverPanic(t *testing.T) {
	t.Parallel()
	reporter := &failureReporter{}
	publisher := &eventPublisherPlugin{
		name: "best-effort-panic-publisher",
	}
	runtimeEngine := plugin.NewRuntime(
		plugin.RuntimeSettings{
			EventFailures: reporter,
		},
	)
	if _, err := runtimeEngine.Start(
		context.Background(),
		&panickingObserver{},
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
	if len(reporter.failures) != 1 ||
		!strings.Contains(reporter.failures[0].Error.Error(), "observer panic") {
		t.Fatalf("reported failures = %#v", reporter.failures)
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
