package plugin

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// EventMode fixes the dispatch semantics of an owner-defined event key.
type EventMode string

const (
	ModeEmit      EventMode = "emit"
	ModeParallel  EventMode = "parallel"
	ModeSerial    EventMode = "serial"
	ModeBail      EventMode = "bail"
	ModeWaterfall EventMode = "waterfall"
)

type eventToken struct {
	marker byte
}

type eventRef struct {
	name  string
	mode  EventMode
	token *eventToken
}

type typedSubscription[T any] struct {
	ordinal uint64
	invoke  T
}

// EventKey is an owner-defined typed event identity. P is the payload and R
// is the decision or waterfall result type; notify events use struct{} for R.
type EventKey[P, R any] struct {
	ref eventRef
}

// DefineEvent creates the canonical key exported by an event owner package.
func DefineEvent[P, R any](canonicalName string, dispatchMode EventMode) EventKey[P, R] {
	if strings.TrimSpace(canonicalName) == "" || canonicalName != strings.TrimSpace(canonicalName) {
		panic("plugin: event name must be non-empty and trimmed")
	}
	switch dispatchMode {
	case ModeEmit, ModeParallel, ModeSerial, ModeBail, ModeWaterfall:
	default:
		panic("plugin: unsupported event mode")
	}
	return EventKey[P, R]{
		ref: eventRef{
			name: canonicalName, mode: dispatchMode,
			token: &eventToken{},
		},
	}
}

// Name returns the canonical event name.
func (topic EventKey[P, R]) Name() string {
	return topic.ref.name
}

// Mode returns the key's fixed dispatch mode.
func (topic EventKey[P, R]) Mode() EventMode {
	return topic.ref.mode
}

// NotifyHandler observes an emit or parallel event.
type NotifyHandler[P any] func(context.Context, P) error

// Decision carries an explicit bail marker separately from R's zero value.
type Decision[R any] struct {
	Value R
	Bail  bool
}

// DecisionHandler handles a serial or bail event.
type DecisionHandler[P, R any] func(context.Context, P) (Decision[R], error)

// Next delegates a waterfall event to the next handler or terminal behavior.
type Next[P, R any] func(context.Context, P) (R, error)

// WaterfallHandler wraps the remainder of a waterfall dispatch.
type WaterfallHandler[P, R any] func(context.Context, P, Next[P, R]) (R, error)

// OnNotify registers an emit or parallel listener as a scope-owned effect.
func OnNotify[P any](pluginScope *Scope, topic EventKey[P, struct{}], callback NotifyHandler[P]) (Disposer, error) {
	if callback == nil {
		return nil, errors.New("plugin: notify listener is nil")
	}
	if topic.ref.mode != ModeEmit && topic.ref.mode != ModeParallel {
		return nil, fmt.Errorf("plugin: event %q uses %s, not notify dispatch", topic.ref.name, topic.ref.mode)
	}
	return registerSubscription(pluginScope, topic.ref, callback)
}

// OnDecision registers a serial or synchronous-bail listener.
func OnDecision[P, R any](pluginScope *Scope, topic EventKey[P, R], callback DecisionHandler[P, R]) (Disposer, error) {
	if callback == nil {
		return nil, errors.New("plugin: decision listener is nil")
	}
	if topic.ref.mode != ModeSerial && topic.ref.mode != ModeBail {
		return nil, fmt.Errorf("plugin: event %q uses %s, not decision dispatch", topic.ref.name, topic.ref.mode)
	}
	return registerSubscription(pluginScope, topic.ref, callback)
}

// OnWaterfall registers an outer-to-inner middleware listener.
func OnWaterfall[P, R any](pluginScope *Scope, topic EventKey[P, R], callback WaterfallHandler[P, R]) (Disposer, error) {
	if callback == nil {
		return nil, errors.New("plugin: waterfall listener is nil")
	}
	if topic.ref.mode != ModeWaterfall {
		return nil, fmt.Errorf("plugin: event %q uses %s, not waterfall dispatch", topic.ref.name, topic.ref.mode)
	}
	return registerSubscription(pluginScope, topic.ref, callback)
}

// Emit invokes listeners synchronously in registration order and joins failures.
func Emit[P any](requestContext context.Context, engine *Runtime, topic EventKey[P, struct{}], payload P) error {
	if topic.ref.mode != ModeEmit {
		return fmt.Errorf("plugin: event %q uses %s, not emit", topic.ref.name, topic.ref.mode)
	}
	callbacks, err := subscriptions[NotifyHandler[P]](engine, topic.ref)
	if err != nil {
		return err
	}
	var dispatchErr error
	for _, callback := range callbacks {
		dispatchErr = errors.Join(dispatchErr, callback(requestContext, payload))
	}
	return dispatchErr
}

// EmitFrom dispatches an emit event through the Runtime that owns sourceScope.
// Long-lived services retain their providing scope instead of receiving a
// second Runtime reference solely for event publication.
func EmitFrom[P any](requestContext context.Context, sourceScope *Scope, topic EventKey[P, struct{}], payload P) error {
	engine, err := runtimeFromScope(sourceScope)
	if err != nil {
		return err
	}
	return Emit(requestContext, engine, topic, payload)
}

// Parallel invokes every listener concurrently, waits for all, and joins failures.
func Parallel[P any](requestContext context.Context, engine *Runtime, topic EventKey[P, struct{}], payload P) error {
	_, err := dispatchParallel(requestContext, engine, topic, payload)
	return err
}

func dispatchParallel[P any](requestContext context.Context, engine *Runtime, topic EventKey[P, struct{}], payload P) (int, error) {
	if topic.ref.mode != ModeParallel {
		return 0, fmt.Errorf("plugin: event %q uses %s, not parallel", topic.ref.name, topic.ref.mode)
	}
	callbacks, err := subscriptions[NotifyHandler[P]](engine, topic.ref)
	if err != nil {
		return 0, err
	}
	errorsByIndex := make([]error, len(callbacks))
	var group sync.WaitGroup
	group.Add(len(callbacks))
	for index, callback := range callbacks {
		go func(listenerIndex int, operation NotifyHandler[P]) {
			defer group.Done()
			errorsByIndex[listenerIndex] = operation(requestContext, payload)
		}(index, callback)
	}
	group.Wait()
	return len(callbacks), errors.Join(errorsByIndex...)
}

// ParallelFrom dispatches a parallel event through the Runtime that owns
// sourceScope and returns how many listeners participated.
func ParallelFrom[P any](requestContext context.Context, sourceScope *Scope, topic EventKey[P, struct{}], payload P) (int, error) {
	engine, err := runtimeFromScope(sourceScope)
	if err != nil {
		return 0, err
	}
	return dispatchParallel(requestContext, engine, topic, payload)
}

// Serial invokes listeners in order and stops at the first explicit bail.
func Serial[P, R any](requestContext context.Context, engine *Runtime, topic EventKey[P, R], payload P) (Decision[R], error) {
	if topic.ref.mode != ModeSerial {
		return Decision[R]{}, fmt.Errorf("plugin: event %q uses %s, not serial", topic.ref.name, topic.ref.mode)
	}
	return dispatchDecision(requestContext, engine, topic, payload)
}

// SerialFrom dispatches a serial event through the Runtime that owns sourceScope.
func SerialFrom[P, R any](requestContext context.Context, sourceScope *Scope, topic EventKey[P, R], payload P) (Decision[R], error) {
	engine, err := runtimeFromScope(sourceScope)
	if err != nil {
		return Decision[R]{}, err
	}
	return Serial(requestContext, engine, topic, payload)
}

// Bail synchronously invokes listeners in order and stops at the first explicit bail.
func Bail[P, R any](requestContext context.Context, engine *Runtime, topic EventKey[P, R], payload P) (Decision[R], error) {
	if topic.ref.mode != ModeBail {
		return Decision[R]{}, fmt.Errorf("plugin: event %q uses %s, not bail", topic.ref.name, topic.ref.mode)
	}
	return dispatchDecision(requestContext, engine, topic, payload)
}

// Waterfall composes listeners outer-to-inner around terminal behavior.
func Waterfall[P, R any](requestContext context.Context, engine *Runtime, topic EventKey[P, R], payload P, terminal Next[P, R]) (R, error) {
	var zero R
	if topic.ref.mode != ModeWaterfall {
		return zero, fmt.Errorf("plugin: event %q uses %s, not waterfall", topic.ref.name, topic.ref.mode)
	}
	if terminal == nil {
		return zero, errors.New("plugin: waterfall terminal is nil")
	}
	callbacks, err := subscriptions[WaterfallHandler[P, R]](engine, topic.ref)
	if err != nil {
		return zero, err
	}
	chain := terminal
	for index := len(callbacks) - 1; index >= 0; index-- {
		callback := callbacks[index]
		downstream := chain
		chain = func(chainContext context.Context, chainPayload P) (R, error) {
			return callback(chainContext, chainPayload, downstream)
		}
	}
	return chain(requestContext, payload)
}

func dispatchDecision[P, R any](requestContext context.Context, engine *Runtime, topic EventKey[P, R], payload P) (Decision[R], error) {
	callbacks, err := subscriptions[DecisionHandler[P, R]](engine, topic.ref)
	if err != nil {
		return Decision[R]{}, err
	}
	for _, callback := range callbacks {
		outcome, callbackErr := callback(requestContext, payload)
		if callbackErr != nil || outcome.Bail {
			return outcome, callbackErr
		}
	}
	return Decision[R]{}, nil
}

func registerSubscription(pluginScope *Scope, definition eventRef, callback any) (Disposer, error) {
	if pluginScope == nil {
		return nil, errors.New("plugin: listen on nil scope")
	}
	pluginScope.owner.mu.Lock()
	if existing, exists := pluginScope.owner.eventDefs[definition.name]; exists && !sameEventRef(existing, definition) {
		pluginScope.owner.mu.Unlock()
		return nil, fmt.Errorf("plugin: event %q was recreated with a different mode or type", definition.name)
	}
	pluginScope.owner.eventDefs[definition.name] = definition
	pluginScope.owner.nextListener++
	ordinal := pluginScope.owner.nextListener
	pluginScope.owner.mu.Unlock()
	return pluginScope.addSubscription(&eventSubscription{ref: definition, invoke: callback, ordinal: ordinal})
}

func subscriptions[T any](engine *Runtime, definition eventRef) ([]T, error) {
	if engine == nil {
		return nil, errors.New("plugin: dispatch on nil runtime")
	}
	engine.mu.RLock()
	existing, exists := engine.eventDefs[definition.name]
	if exists && !sameEventRef(existing, definition) {
		engine.mu.RUnlock()
		return nil, fmt.Errorf("plugin: event %q does not match its registered definition", definition.name)
	}
	registered := make([]typedSubscription[T], 0)
	for _, record := range engine.records {
		// A stopping scope retains each subscription until its own disposer runs.
		// Earlier LIFO cleanup effects may publish their final lifecycle edges.
		if record.state != StateActive && record.state != StateStopping || record.pluginScope == nil {
			continue
		}
		record.pluginScope.mu.Lock()
		for _, subscription := range record.pluginScope.subscriptions {
			if !subscription.owned || !sameEventRef(subscription.ref, definition) {
				continue
			}
			callback, ok := subscription.invoke.(T)
			if !ok {
				record.pluginScope.mu.Unlock()
				engine.mu.RUnlock()
				return nil, fmt.Errorf("plugin: event %q listener has an incompatible Go type", definition.name)
			}
			registered = append(registered, typedSubscription[T]{ordinal: subscription.ordinal, invoke: callback})
		}
		record.pluginScope.mu.Unlock()
	}
	engine.mu.RUnlock()
	sort.SliceStable(registered, func(leftIndex int, rightIndex int) bool {
		return registered[leftIndex].ordinal < registered[rightIndex].ordinal
	})
	typedCallbacks := make([]T, 0, len(registered))
	for _, registeredListener := range registered {
		typedCallbacks = append(typedCallbacks, registeredListener.invoke)
	}
	return typedCallbacks, nil
}

func sameEventRef(left eventRef, right eventRef) bool {
	return left.name == right.name && left.mode == right.mode && left.token == right.token
}

func runtimeFromScope(sourceScope *Scope) (*Runtime, error) {
	if sourceScope == nil || sourceScope.owner == nil {
		return nil, errors.New("plugin: dispatch from nil scope")
	}
	return sourceScope.owner, nil
}
