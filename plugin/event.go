package plugin

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// DeliveryPolicy controls only fact-delivery mechanics. It never gives Event
// observers veto or result-transformation semantics.
type DeliveryPolicy uint8

const (
	DeliveryOrdered DeliveryPolicy = iota
	DeliveryParallel
	DeliveryBestEffort
)

type eventToken struct {
	marker byte
}

type eventRef struct {
	name   string
	policy DeliveryPolicy
	token  *eventToken
}

func (definitionRef eventRef) sameDefinition(otherRef eventRef) bool {
	return definitionRef.name == otherRef.name &&
		definitionRef.policy == otherRef.policy &&
		definitionRef.token == otherRef.token
}

// EventObserver observes one fact after its owner has logically committed it.
type EventObserver[E Event] interface {
	ObserveEvent(requestContext context.Context, fact E) error
}

// EventFailure describes one best-effort observer failure.
type EventFailure struct {
	EventName string
	Error     error
}

// EventFailureReporter receives failures from best-effort Event delivery.
type EventFailureReporter interface {
	ReportEventFailure(requestContext context.Context, failure EventFailure)
}

// EventDefinition is the owner-defined typed identity and delivery policy of
// one fact stream.
type EventDefinition[E Event] struct {
	ref eventRef
}

// DefineEvent creates one canonical typed Event definition.
func DefineEvent[E Event](
	canonicalName string,
	policy DeliveryPolicy,
) EventDefinition[E] {
	if strings.TrimSpace(canonicalName) == "" || canonicalName != strings.TrimSpace(canonicalName) {
		panic("plugin: Event name must be non-empty and trimmed")
	}
	switch policy {
	case DeliveryOrdered, DeliveryParallel, DeliveryBestEffort:
	default:
		panic("plugin: unsupported Event delivery policy")
	}
	return EventDefinition[E]{
		ref: eventRef{
			name:   canonicalName,
			policy: policy,
			token:  &eventToken{},
		},
	}
}

// Name returns the canonical Event name.
func (definition EventDefinition[E]) Name() string {
	return definition.ref.name
}

// Observe installs one Fiber-owned Observer subscription.
func (definition EventDefinition[E]) Observe(
	pluginContext *Context,
	observer EventObserver[E],
) error {
	if pluginContext == nil {
		return errors.New("plugin: observe Event through nil Context")
	}
	subscription := &eventSubscriptionOf[E]{
		definition: definition,
		observer:   observer,
	}
	return pluginContext.register(subscription)
}

// Publish snapshots the observers admitted by sourceScope and delivers one
// already committed fact according to the Definition policy.
func (definition EventDefinition[E]) Publish(
	requestContext context.Context,
	sourceScope *Scope,
	fact E,
) error {
	if sourceScope == nil || sourceScope.runtime == nil || sourceScope.isClosed() {
		return errors.New("plugin: publish Event through nil Scope")
	}
	observers, err := snapshotEvent(definition, sourceScope)
	if err != nil {
		return err
	}
	switch definition.ref.policy {
	case DeliveryOrdered:
		var deliveryErr error
		for _, observer := range observers {
			deliveryErr = errors.Join(deliveryErr, observeEvent(requestContext, observer, fact))
		}
		return deliveryErr
	case DeliveryParallel:
		failures := make([]error, len(observers))
		var deliveryGroup sync.WaitGroup
		deliveryGroup.Add(len(observers))
		for observerIndex, observer := range observers {
			go func(selectedIndex int, selectedObserver EventObserver[E]) {
				defer deliveryGroup.Done()
				failures[selectedIndex] = observeEvent(requestContext, selectedObserver, fact)
			}(observerIndex, observer)
		}
		deliveryGroup.Wait()
		return errors.Join(failures...)
	case DeliveryBestEffort:
		for _, observer := range observers {
			observerErr := observeEvent(requestContext, observer, fact)
			if observerErr == nil || sourceScope.runtime.eventFailures == nil {
				continue
			}
			sourceScope.runtime.eventFailures.ReportEventFailure(
				requestContext,
				EventFailure{
					EventName: definition.ref.name,
					Error:     observerErr,
				},
			)
		}
		return nil
	default:
		return errors.New("plugin: unsupported Event delivery policy")
	}
}

func observeEvent[E Event](
	requestContext context.Context,
	observer EventObserver[E],
	fact E,
) (observerErr error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			observerErr = fmt.Errorf("plugin: Event observer panicked: %v", recovered)
		}
	}()
	return observer.ObserveEvent(requestContext, fact)
}

type eventSubscription interface {
	runtimeEntry
	eventDefinitionRef() eventRef
}

type eventSubscriptionOf[E Event] struct {
	definition EventDefinition[E]
	observer   EventObserver[E]
	ordinal    uint64
	owner      *fiberEffect
}

func (subscription *eventSubscriptionOf[E]) eventDefinitionRef() eventRef {
	return subscription.definition.ref
}

func (subscription *eventSubscriptionOf[E]) Label() string {
	return "observe:" + subscription.definition.ref.name
}

func (subscription *eventSubscriptionOf[E]) validateEntry(ownership *fiberEffect) error {
	registry := ownership.runtime.events
	if existingRef, exists := registry.definitions[subscription.definition.ref.name]; exists &&
		!existingRef.sameDefinition(subscription.definition.ref) {
		return fmt.Errorf(
			"plugin: Event %q was recreated with a different definition",
			subscription.definition.ref.name,
		)
	}
	if existingBucket, exists := registry.buckets[subscription.definition.ref.name]; exists {
		if _, matches := existingBucket.(*eventBucketOf[E]); !matches {
			return fmt.Errorf(
				"plugin: Event %q has an incompatible typed bucket",
				subscription.definition.ref.name,
			)
		}
	}
	return nil
}

func (subscription *eventSubscriptionOf[E]) publishEntry(ownership *fiberEffect) {
	registry := ownership.runtime.events
	registry.definitions[subscription.definition.ref.name] = subscription.definition.ref
	typedBucket, exists := registry.buckets[subscription.definition.ref.name].(*eventBucketOf[E])
	if !exists {
		typedBucket = &eventBucketOf[E]{
			definition: subscription.definition,
		}
		registry.buckets[subscription.definition.ref.name] = typedBucket
	}
	if subscription.ordinal == 0 {
		registry.nextOrdinal++
		subscription.ordinal = registry.nextOrdinal
	}
	subscription.owner = ownership
	typedBucket.subscriptions = append(typedBucket.subscriptions, subscription)
}

func (subscription *eventSubscriptionOf[E]) withdrawEntry(ownership *fiberEffect) {
	registry := ownership.runtime.events
	typedBucket, exists := registry.buckets[subscription.definition.ref.name].(*eventBucketOf[E])
	if !exists {
		return
	}
	for subscriptionIndex, registeredSubscription := range typedBucket.subscriptions {
		if registeredSubscription != subscription {
			continue
		}
		typedBucket.subscriptions = append(
			typedBucket.subscriptions[:subscriptionIndex],
			typedBucket.subscriptions[subscriptionIndex+1:]...,
		)
		break
	}
	subscription.owner = nil
}

func (subscription *eventSubscriptionOf[E]) diagnostic() runtimeEntryDiagnostic {
	return runtimeEntryDiagnostic{
		kind: runtimeEntryEvent,
		name: subscription.definition.ref.name,
	}
}

type eventBucket interface {
	eventDefinitionRef() eventRef
}

type eventBucketOf[E Event] struct {
	definition    EventDefinition[E]
	subscriptions []*eventSubscriptionOf[E]
}

func (bucket *eventBucketOf[E]) eventDefinitionRef() eventRef {
	return bucket.definition.ref
}

// eventRegistry owns typed Observer buckets and subscription order.
// Runtime.state protects its fields; snapshots escape before external calls.
type eventRegistry struct {
	definitions map[string]eventRef
	buckets     map[string]eventBucket
	nextOrdinal uint64
}

func newEventRegistry() *eventRegistry {
	return &eventRegistry{
		definitions: make(map[string]eventRef),
		buckets:     make(map[string]eventBucket),
	}
}

func snapshotEvent[E Event](
	definition EventDefinition[E],
	sourceScope *Scope,
) ([]EventObserver[E], error) {
	runtimeEngine := sourceScope.runtime
	runtimeEngine.state.RLock()
	defer runtimeEngine.state.RUnlock()
	if existingRef, exists := runtimeEngine.events.definitions[definition.ref.name]; exists &&
		!existingRef.sameDefinition(definition.ref) {
		return nil, fmt.Errorf(
			"plugin: Event %q does not match its registered definition",
			definition.ref.name,
		)
	}
	existingBucket, exists := runtimeEngine.events.buckets[definition.ref.name]
	if !exists {
		return nil, nil
	}
	typedBucket, matches := existingBucket.(*eventBucketOf[E])
	if !matches {
		return nil, fmt.Errorf(
			"plugin: Event %q has an incompatible typed bucket",
			definition.ref.name,
		)
	}
	lineage := scopePath(sourceScope)
	snapshot := make([]EventObserver[E], 0, len(typedBucket.subscriptions))
	for _, selectedScope := range lineage {
		selectedSubscriptions := make([]*eventSubscriptionOf[E], 0)
		for _, subscription := range typedBucket.subscriptions {
			if subscription.owner == nil || subscription.owner.state != fiberEffectActive ||
				subscription.owner.fiber.state != FiberActive ||
				subscription.owner.scope.target != selectedScope.target {
				continue
			}
			selectedSubscriptions = append(selectedSubscriptions, subscription)
		}
		sort.Slice(selectedSubscriptions, func(leftIndex int, rightIndex int) bool {
			return selectedSubscriptions[leftIndex].ordinal < selectedSubscriptions[rightIndex].ordinal
		})
		for _, subscription := range selectedSubscriptions {
			snapshot = append(snapshot, subscription.observer)
		}
	}
	return snapshot, nil
}
