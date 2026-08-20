package plugin

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
)

type waterfallToken struct {
	marker byte
}

// waterfallRef is the private type-erased identity used by Runtime registries.
type waterfallRef struct {
	name  string
	token *waterfallToken
}

func (definitionRef waterfallRef) sameDefinition(otherRef waterfallRef) bool {
	return definitionRef.name == otherRef.name && definitionRef.token == otherRef.token
}

// WaterfallTerminal owns the unintercepted behavior of one operation.
type WaterfallTerminal[I WaterfallInput, O WaterfallOutput] interface {
	Execute(requestContext context.Context, input I) (O, error)
}

// WaterfallNext delegates to the next Middleware or terminal operation.
type WaterfallNext[I WaterfallInput, O WaterfallOutput] interface {
	Proceed(requestContext context.Context, input I) (O, error)
}

// WaterfallMiddleware wraps one owner-defined operation. It may transform the
// input, short-circuit, or wrap the downstream result and error.
type WaterfallMiddleware[I WaterfallInput, O WaterfallOutput] interface {
	Intercept(
		requestContext context.Context,
		input I,
		next WaterfallNext[I, O],
	) (O, error)
}

// WaterfallDefinition is the owner-defined typed identity of one interceptable
// operation.
type WaterfallDefinition[I WaterfallInput, O WaterfallOutput] struct {
	ref waterfallRef
}

// DefineWaterfall creates one canonical typed Waterfall definition.
func DefineWaterfall[I WaterfallInput, O WaterfallOutput](
	canonicalName string,
) WaterfallDefinition[I, O] {
	if strings.TrimSpace(canonicalName) == "" || canonicalName != strings.TrimSpace(canonicalName) {
		panic("plugin: Waterfall name must be non-empty and trimmed")
	}
	return WaterfallDefinition[I, O]{
		ref: waterfallRef{
			name:  canonicalName,
			token: &waterfallToken{},
		},
	}
}

// Name returns the canonical Waterfall name.
func (definition WaterfallDefinition[I, O]) Name() string {
	return definition.ref.name
}

// Use installs one Fiber-owned Middleware binding.
func (definition WaterfallDefinition[I, O]) Use(
	pluginContext *Context,
	middleware WaterfallMiddleware[I, O],
) error {
	if pluginContext == nil {
		return errors.New("plugin: use Waterfall through nil Context")
	}
	binding := &waterfallBindingOf[I, O]{
		definition: definition,
		middleware: middleware,
	}
	return pluginContext.register(binding)
}

// Run snapshots admitted Middleware from root to source Scope and executes the
// typed onion chain around terminal.
func (definition WaterfallDefinition[I, O]) Run(
	requestContext context.Context,
	sourceScope *Scope,
	input I,
	terminal WaterfallTerminal[I, O],
) (O, error) {
	var output O
	if sourceScope == nil || sourceScope.runtime == nil || sourceScope.isClosed() {
		return output, errors.New("plugin: run Waterfall through nil Scope")
	}
	if terminal == nil {
		return output, errors.New("plugin: Waterfall terminal is nil")
	}
	middleware, err := snapshotWaterfall(definition, sourceScope)
	if err != nil {
		return output, err
	}
	chain := &waterfallChain[I, O]{
		middleware: middleware,
		terminal:   terminal,
	}
	return chain.Proceed(requestContext, input)
}

type waterfallBinding interface {
	runtimeEntry
	waterfallDefinitionRef() waterfallRef
}

type waterfallBindingOf[I WaterfallInput, O WaterfallOutput] struct {
	definition WaterfallDefinition[I, O]
	middleware WaterfallMiddleware[I, O]
	ordinal    uint64
	owner      *fiberEffect
}

func (binding *waterfallBindingOf[I, O]) waterfallDefinitionRef() waterfallRef {
	return binding.definition.ref
}

func (binding *waterfallBindingOf[I, O]) Label() string {
	return "waterfall:" + binding.definition.ref.name
}

func (binding *waterfallBindingOf[I, O]) validateEntry(ownership *fiberEffect) error {
	registry := ownership.runtime.waterfalls
	if existingRef, exists := registry.definitions[binding.definition.ref.name]; exists &&
		!existingRef.sameDefinition(binding.definition.ref) {
		return fmt.Errorf(
			"plugin: Waterfall %q was recreated with a different definition",
			binding.definition.ref.name,
		)
	}
	if existingBucket, exists := registry.buckets[binding.definition.ref.name]; exists {
		if _, matches := existingBucket.(*waterfallBucketOf[I, O]); !matches {
			return fmt.Errorf(
				"plugin: Waterfall %q has an incompatible typed bucket",
				binding.definition.ref.name,
			)
		}
	}
	return nil
}

func (binding *waterfallBindingOf[I, O]) publishEntry(ownership *fiberEffect) {
	registry := ownership.runtime.waterfalls
	registry.definitions[binding.definition.ref.name] = binding.definition.ref
	typedBucket, exists := registry.buckets[binding.definition.ref.name].(*waterfallBucketOf[I, O])
	if !exists {
		typedBucket = &waterfallBucketOf[I, O]{
			definition: binding.definition,
		}
		registry.buckets[binding.definition.ref.name] = typedBucket
	}
	if binding.ordinal == 0 {
		registry.nextOrdinal++
		binding.ordinal = registry.nextOrdinal
	}
	binding.owner = ownership
	typedBucket.bindings = append(typedBucket.bindings, binding)
}

func (binding *waterfallBindingOf[I, O]) withdrawEntry(ownership *fiberEffect) {
	registry := ownership.runtime.waterfalls
	typedBucket, exists := registry.buckets[binding.definition.ref.name].(*waterfallBucketOf[I, O])
	if !exists {
		return
	}
	for bindingIndex, registeredBinding := range typedBucket.bindings {
		if registeredBinding != binding {
			continue
		}
		typedBucket.bindings = append(
			typedBucket.bindings[:bindingIndex],
			typedBucket.bindings[bindingIndex+1:]...,
		)
		break
	}
	binding.owner = nil
}

func (binding *waterfallBindingOf[I, O]) diagnostic() runtimeEntryDiagnostic {
	return runtimeEntryDiagnostic{
		kind: runtimeEntryWaterfall,
		name: binding.definition.ref.name,
	}
}

type waterfallBucket interface {
	waterfallDefinitionRef() waterfallRef
}

type waterfallBucketOf[I WaterfallInput, O WaterfallOutput] struct {
	definition WaterfallDefinition[I, O]
	bindings   []*waterfallBindingOf[I, O]
}

func (bucket *waterfallBucketOf[I, O]) waterfallDefinitionRef() waterfallRef {
	return bucket.definition.ref
}

// waterfallRegistry owns typed Middleware buckets and registration order.
// Runtime.state protects its fields; snapshots escape before external calls.
type waterfallRegistry struct {
	definitions map[string]waterfallRef
	buckets     map[string]waterfallBucket
	nextOrdinal uint64
}

func newWaterfallRegistry() *waterfallRegistry {
	return &waterfallRegistry{
		definitions: make(map[string]waterfallRef),
		buckets:     make(map[string]waterfallBucket),
	}
}

type waterfallChain[I WaterfallInput, O WaterfallOutput] struct {
	middleware []WaterfallMiddleware[I, O]
	terminal   WaterfallTerminal[I, O]
	index      int
	proceeded  atomic.Bool
}

func (chain *waterfallChain[I, O]) Proceed(
	requestContext context.Context,
	input I,
) (O, error) {
	var output O
	if !chain.proceeded.CompareAndSwap(false, true) {
		return output, ErrWaterfallAlreadyProceeded
	}
	if chain.index >= len(chain.middleware) {
		return chain.terminal.Execute(requestContext, input)
	}
	nextChain := &waterfallChain[I, O]{
		middleware: chain.middleware,
		terminal:   chain.terminal,
		index:      chain.index + 1,
	}
	return chain.middleware[chain.index].Intercept(requestContext, input, nextChain)
}

func snapshotWaterfall[I WaterfallInput, O WaterfallOutput](
	definition WaterfallDefinition[I, O],
	sourceScope *Scope,
) ([]WaterfallMiddleware[I, O], error) {
	runtimeEngine := sourceScope.runtime
	runtimeEngine.state.RLock()
	defer runtimeEngine.state.RUnlock()
	if existingRef, exists := runtimeEngine.waterfalls.definitions[definition.ref.name]; exists &&
		!existingRef.sameDefinition(definition.ref) {
		return nil, fmt.Errorf(
			"plugin: Waterfall %q does not match its registered definition",
			definition.ref.name,
		)
	}
	existingBucket, exists := runtimeEngine.waterfalls.buckets[definition.ref.name]
	if !exists {
		return nil, nil
	}
	typedBucket, matches := existingBucket.(*waterfallBucketOf[I, O])
	if !matches {
		return nil, fmt.Errorf(
			"plugin: Waterfall %q has an incompatible typed bucket",
			definition.ref.name,
		)
	}
	lineage := scopePath(sourceScope)
	snapshot := make([]WaterfallMiddleware[I, O], 0, len(typedBucket.bindings))
	for scopeIndex := len(lineage) - 1; scopeIndex >= 0; scopeIndex-- {
		selectedKey := lineage[scopeIndex].target
		selectedBindings := make([]*waterfallBindingOf[I, O], 0)
		for _, binding := range typedBucket.bindings {
			if binding.owner == nil || binding.owner.state != fiberEffectActive ||
				binding.owner.fiber.state != FiberActive || binding.owner.scope.target != selectedKey {
				continue
			}
			selectedBindings = append(selectedBindings, binding)
		}
		sort.Slice(selectedBindings, func(leftIndex int, rightIndex int) bool {
			return selectedBindings[leftIndex].ordinal < selectedBindings[rightIndex].ordinal
		})
		for _, binding := range selectedBindings {
			snapshot = append(snapshot, binding.middleware)
		}
	}
	return snapshot, nil
}
