package plugin

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
)

// DeliveryPolicy controls fact delivery mechanics only.
type DeliveryPolicy uint8

const (
	// DeliveryOrdered invokes matching Observers sequentially.
	DeliveryOrdered DeliveryPolicy = iota
	// DeliveryParallel invokes matching Observers concurrently and joins errors.
	DeliveryParallel
	// DeliveryBestEffort reports Observer failures and returns success.
	DeliveryBestEffort
)

// EventObserver is the single event entry point of a Plugin. Runtime invokes
// it only for Event types declared by that Plugin's Manifest.
type EventObserver interface {
	ObserveEvent(context.Context, Event) error
}

// EventFailure describes one best-effort Observer failure.
type EventFailure struct {
	EventName string
	Error     error
}

// EventFailureReporter receives failures suppressed by DeliveryBestEffort.
type EventFailureReporter interface {
	ReportEventFailure(context.Context, EventFailure)
}

type typedEventSubscription[E Event] struct{}

// EventOf declares that a Plugin observes one Event type through its unified
// EventObserver entry point.
func EventOf[E Event]() EventSubscription {
	return typedEventSubscription[E]{}
}

func (typedEventSubscription[E]) Name() string {
	reference, err := eventReferenceOf[E]()
	if err != nil {
		return namedTypeName(reflect.TypeFor[E]())
	}
	return reference.name
}

func (typedEventSubscription[E]) eventReference() (eventRef, error) {
	return eventReferenceOf[E]()
}

func (typedEventSubscription[E]) bindEventObserver(
	pluginInstance Plugin,
) (EventObserver, error) {
	observer, matches := pluginInstance.(EventObserver)
	if !matches {
		return nil, errors.New("Plugin does not implement EventObserver")
	}
	return observer, nil
}

func eventReferenceOf[E Event]() (reference eventRef, referenceErr error) {
	selectedType := reflect.TypeFor[E]()
	if selectedType == nil || selectedType.Kind() != reflect.Struct || selectedType.Name() == "" {
		return eventRef{}, errors.New("Event must be a named struct value")
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			reference = eventRef{}
			referenceErr = fmt.Errorf("Event metadata panicked: %v", recovered)
		}
	}()
	var fact E
	eventName := fact.EventName()
	if strings.TrimSpace(eventName) == "" || eventName != strings.TrimSpace(eventName) {
		return eventRef{}, errors.New("Event name must be non-empty and trimmed")
	}
	policy := fact.EventDelivery()
	if err := validateDeliveryPolicy(policy); err != nil {
		return eventRef{}, err
	}
	return eventRef{
		key:    selectedType,
		name:   eventName,
		policy: policy,
	}, nil
}

func validateDeliveryPolicy(policy DeliveryPolicy) error {
	switch policy {
	case DeliveryOrdered, DeliveryParallel, DeliveryBestEffort:
		return nil
	default:
		return errors.New("unsupported Event delivery policy")
	}
}

type eventBinding struct {
	reference eventRef
	observer  EventObserver
	owner     *fiber
	scope     *scope
	ordinal   uint64
}

type eventRegistry struct {
	bindings map[reflect.Type][]*eventBinding
}

func newEventRegistry() *eventRegistry {
	return &eventRegistry{
		bindings: make(map[reflect.Type][]*eventBinding),
	}
}

func (registry *eventRegistry) add(binding *eventBinding) {
	registry.bindings[binding.reference.key] = append(
		registry.bindings[binding.reference.key],
		binding,
	)
}

func (registry *eventRegistry) remove(binding *eventBinding) {
	candidates := registry.bindings[binding.reference.key]
	for candidateIndex, candidate := range candidates {
		if candidate != binding {
			continue
		}
		registry.bindings[binding.reference.key] = append(
			candidates[:candidateIndex],
			candidates[candidateIndex+1:]...,
		)
		break
	}
}

func (registry *eventRegistry) snapshot(
	reference eventRef,
	sourceScope *scope,
) []EventObserver {
	lineage := scopePath(sourceScope)
	observers := make([]EventObserver, 0)
	for _, selectedScope := range lineage {
		selectedBindings := make([]*eventBinding, 0)
		for _, binding := range registry.bindings[reference.key] {
			if binding.scope != selectedScope || binding.owner == nil ||
				binding.owner.state != FiberActive {
				continue
			}
			selectedBindings = append(selectedBindings, binding)
		}
		sort.Slice(selectedBindings, func(leftIndex int, rightIndex int) bool {
			return selectedBindings[leftIndex].ordinal < selectedBindings[rightIndex].ordinal
		})
		for _, binding := range selectedBindings {
			observers = append(observers, binding.observer)
		}
	}
	return observers
}

// eventDispatcher owns Event subscriptions, delivery policy execution, and
// best-effort failure reporting. Runtime.view guards its registry operations;
// delivery runs after the caller releases that lock.
type eventDispatcher struct {
	registry *eventRegistry
	failures EventFailureReporter
}

func newEventDispatcher(failures EventFailureReporter) *eventDispatcher {
	return &eventDispatcher{
		registry: newEventRegistry(),
		failures: failures,
	}
}

func (dispatcher *eventDispatcher) validateDeliveryRequirements(
	subscriptions []eventSubscriptionSpec,
) error {
	if dispatcher.failures != nil {
		return nil
	}
	for _, subscription := range subscriptions {
		if subscription.reference.policy == DeliveryBestEffort {
			return fmt.Errorf(
				"plugin: Event %q requires an EventFailureReporter for best-effort delivery",
				subscription.reference.name,
			)
		}
	}
	return nil
}

// Runtime.view must be locked by the caller.
func (dispatcher *eventDispatcher) validateMetadata(
	subscriptions []eventSubscriptionSpec,
) error {
	for _, subscription := range subscriptions {
		for _, existingBinding := range dispatcher.registry.bindings[subscription.reference.key] {
			if existingBinding.reference.name == subscription.reference.name &&
				existingBinding.reference.policy == subscription.reference.policy {
				continue
			}
			return fmt.Errorf(
				"plugin: Event type %q has inconsistent metadata",
				namedTypeName(subscription.reference.key),
			)
		}
	}
	return nil
}

// Runtime.view must be write-locked by the caller.
func (dispatcher *eventDispatcher) subscribe(binding *eventBinding) {
	dispatcher.registry.add(binding)
}

// Runtime.view must be write-locked by the caller.
func (dispatcher *eventDispatcher) unsubscribe(binding *eventBinding) {
	dispatcher.registry.remove(binding)
}

// Runtime.view must be read-locked by the caller.
func (dispatcher *eventDispatcher) snapshotObservers(
	reference eventRef,
	sourceScope *scope,
) []EventObserver {
	return dispatcher.registry.snapshot(reference, sourceScope)
}

func (dispatcher *eventDispatcher) deliver(
	requestContext context.Context,
	fact Event,
	reference eventRef,
	observers []EventObserver,
) error {
	switch reference.policy {
	case DeliveryOrdered:
		for _, observer := range observers {
			if err := invokeEventObserver(requestContext, fact, observer); err != nil {
				return err
			}
		}
		return nil
	case DeliveryParallel:
		return observeEventParallel(requestContext, fact, observers, nil)
	case DeliveryBestEffort:
		return observeEventParallel(
			requestContext,
			fact,
			observers,
			func(observerErr error) {
				dispatcher.failures.ReportEventFailure(
					requestContext,
					EventFailure{
						EventName: reference.name,
						Error:     observerErr,
					},
				)
			},
		)
	default:
		return errors.New("plugin: unsupported Event delivery policy")
	}
}

// Publish delivers fact from the active source Plugin to matching Observers in
// the source Scope and its ancestors.
func Publish[E Event](
	requestContext context.Context,
	source Plugin,
	fact E,
) error {
	sourceFiber, err := activeFiberOf(source)
	if err != nil {
		return err
	}
	reference, err := eventReferenceOf[E]()
	if err != nil {
		return fmt.Errorf("plugin: publish Event: %w", err)
	}
	if fact.EventName() != reference.name || fact.EventDelivery() != reference.policy {
		return errors.New("plugin: Event metadata must be constant for one Go type")
	}
	runtimeEngine := sourceFiber.runtime
	runtimeEngine.view.RLock()
	if sourceFiber.state != FiberActive {
		runtimeEngine.view.RUnlock()
		return ErrPluginNotActive
	}
	observers := runtimeEngine.bindings.events.snapshotObservers(
		reference,
		sourceFiber.scope,
	)
	runtimeEngine.view.RUnlock()
	return runtimeEngine.bindings.events.deliver(
		requestContext,
		fact,
		reference,
		observers,
	)
}

func observeEventParallel(
	requestContext context.Context,
	fact Event,
	observers []EventObserver,
	reportFailure func(error),
) error {
	failures := make([]error, len(observers))
	var deliveries sync.WaitGroup
	deliveries.Add(len(observers))
	for observerIndex, selectedObserver := range observers {
		go func(selectedIndex int, observer EventObserver) {
			defer deliveries.Done()
			failures[selectedIndex] = invokeEventObserver(requestContext, fact, observer)
		}(observerIndex, selectedObserver)
	}
	deliveries.Wait()
	if reportFailure != nil {
		for _, observerErr := range failures {
			if observerErr != nil {
				reportFailure(observerErr)
			}
		}
		return nil
	}
	return errors.Join(failures...)
}

func invokeEventObserver(
	requestContext context.Context,
	fact Event,
	observer EventObserver,
) (observerErr error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			switch failure := recovered.(type) {
			case error:
				observerErr = fmt.Errorf("plugin: Event observer panicked: %w", failure)
			default:
				observerErr = fmt.Errorf("plugin: Event observer panicked: %v", failure)
			}
		}
	}()
	return observer.ObserveEvent(requestContext, fact)
}
