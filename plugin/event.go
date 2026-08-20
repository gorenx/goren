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

// Publish delivers fact from the active source Plugin to matching Observers in
// the source Scope and its ancestors.
func Publish[E Event](
	requestContext context.Context,
	source Plugin,
	fact E,
) error {
	selectedActivation, err := activeActivationOf(source)
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
	runtimeEngine := selectedActivation.runtime
	runtimeEngine.state.RLock()
	if selectedActivation.fiber.state != FiberActive {
		runtimeEngine.state.RUnlock()
		return ErrPluginNotActive
	}
	observers := runtimeEngine.events.snapshot(
		reference,
		selectedActivation.fiber.scope,
	)
	runtimeEngine.state.RUnlock()

	switch reference.policy {
	case DeliveryOrdered:
		for _, observer := range observers {
			if err := observer.ObserveEvent(requestContext, fact); err != nil {
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
				if runtimeEngine.eventFailures == nil {
					return
				}
				runtimeEngine.eventFailures.ReportEventFailure(
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
			failures[selectedIndex] = observer.ObserveEvent(requestContext, fact)
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
