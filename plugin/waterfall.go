package plugin

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"sync/atomic"
)

// WaterfallAction is one executable downstream operation. The business
// terminal and each Runtime-assembled Middleware step implement the same role.
type WaterfallAction[I WaterfallInput, O WaterfallOutput] interface {
	Execute(context.Context, I) (O, error)
}

// WaterfallMiddleware wraps one typed operation.
type WaterfallMiddleware[I WaterfallInput, O WaterfallOutput] interface {
	Intercept(context.Context, I, WaterfallAction[I, O]) (O, error)
}

type typedWaterfallMiddlewareBinding[I WaterfallInput, O WaterfallOutput] struct {
	middleware WaterfallMiddleware[I, O]
}

// WaterfallOf declares one Middleware object owned by the Plugin Manifest.
func WaterfallOf[I WaterfallInput, O WaterfallOutput](
	middleware WaterfallMiddleware[I, O],
) WaterfallMiddlewareBinding {
	return typedWaterfallMiddlewareBinding[I, O]{
		middleware: middleware,
	}
}

func (typedWaterfallMiddlewareBinding[I, O]) Name() string {
	reference, err := waterfallReferenceOf[I, O]()
	if err != nil {
		return ""
	}
	return reference.name
}

func (typedWaterfallMiddlewareBinding[I, O]) waterfallReference() (waterfallRef, error) {
	return waterfallReferenceOf[I, O]()
}

func (typedBinding typedWaterfallMiddlewareBinding[I, O]) waterfallMiddleware() (waterfallInvoker, error) {
	if typedBinding.middleware == nil {
		return nil, errors.New("Plugin declared a nil WaterfallMiddleware")
	}
	return waterfallInvokerOf[I, O]{
		middleware: typedBinding.middleware,
	}, nil
}

func waterfallReferenceOf[
	I WaterfallInput,
	O WaterfallOutput,
]() (waterfallRef, error) {
	inputType := reflect.TypeFor[I]()
	outputType := reflect.TypeFor[O]()
	if inputType == nil || inputType.Name() == "" {
		return waterfallRef{}, errors.New("Waterfall input must be a named type")
	}
	if outputType == nil || outputType.Name() == "" {
		return waterfallRef{}, errors.New("Waterfall output must be a named type")
	}
	return waterfallRef{
		input:  inputType,
		output: outputType,
		name:   namedTypeName(inputType) + " -> " + namedTypeName(outputType),
	}, nil
}

type waterfallKey struct {
	input  reflect.Type
	output reflect.Type
}

type waterfallInvoker interface {
	waterfallTypes() waterfallKey
}

type waterfallInvokerOf[I WaterfallInput, O WaterfallOutput] struct {
	middleware WaterfallMiddleware[I, O]
}

func (waterfallInvokerOf[I, O]) waterfallTypes() waterfallKey {
	return waterfallKey{
		input:  reflect.TypeFor[I](),
		output: reflect.TypeFor[O](),
	}
}

type waterfallBinding struct {
	reference waterfallRef
	invoker   waterfallInvoker
	owner     *fiber
	scope     *scope
	ordinal   uint64
}

type waterfallRegistry struct {
	bindings map[waterfallKey][]*waterfallBinding
}

func newWaterfallRegistry() *waterfallRegistry {
	return &waterfallRegistry{
		bindings: make(map[waterfallKey][]*waterfallBinding),
	}
}

func (registry *waterfallRegistry) add(binding *waterfallBinding) {
	selectedKey := waterfallKey{
		input:  binding.reference.input,
		output: binding.reference.output,
	}
	registry.bindings[selectedKey] = append(
		registry.bindings[selectedKey],
		binding,
	)
}

func (registry *waterfallRegistry) remove(binding *waterfallBinding) {
	selectedKey := waterfallKey{
		input:  binding.reference.input,
		output: binding.reference.output,
	}
	candidates := registry.bindings[selectedKey]
	for candidateIndex, candidate := range candidates {
		if candidate != binding {
			continue
		}
		registry.bindings[selectedKey] = append(
			candidates[:candidateIndex],
			candidates[candidateIndex+1:]...,
		)
		break
	}
}

func snapshotWaterfall[
	I WaterfallInput,
	O WaterfallOutput,
](
	registry *waterfallRegistry,
	sourceScope *scope,
) ([]WaterfallMiddleware[I, O], error) {
	selectedKey := waterfallKey{
		input:  reflect.TypeFor[I](),
		output: reflect.TypeFor[O](),
	}
	lineage := scopePath(sourceScope)
	middleware := make([]WaterfallMiddleware[I, O], 0)
	for scopeIndex := len(lineage) - 1; scopeIndex >= 0; scopeIndex-- {
		selectedScope := lineage[scopeIndex]
		selectedBindings := make([]*waterfallBinding, 0)
		for _, binding := range registry.bindings[selectedKey] {
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
			typedInvoker, matches := binding.invoker.(waterfallInvokerOf[I, O])
			if !matches {
				return nil, errors.New("plugin: Waterfall has an incompatible Middleware")
			}
			middleware = append(middleware, typedInvoker.middleware)
		}
	}
	return middleware, nil
}

type waterfallStep[I WaterfallInput, O WaterfallOutput] struct {
	middleware []WaterfallMiddleware[I, O]
	terminal   WaterfallAction[I, O]
	index      int
	proceeded  atomic.Bool
}

func (step *waterfallStep[I, O]) Execute(
	requestContext context.Context,
	input I,
) (O, error) {
	var output O
	if !step.proceeded.CompareAndSwap(false, true) {
		return output, ErrWaterfallAlreadyExecuted
	}
	if step.index >= len(step.middleware) {
		return step.terminal.Execute(requestContext, input)
	}
	downstream := &waterfallStep[I, O]{
		middleware: step.middleware,
		terminal:   step.terminal,
		index:      step.index + 1,
	}
	return invokeWaterfallMiddleware(
		requestContext,
		input,
		step.middleware[step.index],
		downstream,
	)
}

func invokeWaterfallMiddleware[
	I WaterfallInput,
	O WaterfallOutput,
](
	requestContext context.Context,
	input I,
	middleware WaterfallMiddleware[I, O],
	downstream WaterfallAction[I, O],
) (output O, middlewareErr error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			switch failure := recovered.(type) {
			case error:
				middlewareErr = fmt.Errorf(
					"plugin: Waterfall Middleware panicked: %w",
					failure,
				)
			default:
				middlewareErr = fmt.Errorf(
					"plugin: Waterfall Middleware panicked: %v",
					failure,
				)
			}
		}
	}()
	return middleware.Intercept(requestContext, input, downstream)
}

// Run executes the typed onion chain visible from source around terminal.
func Run[
	I WaterfallInput,
	O WaterfallOutput,
](
	requestContext context.Context,
	source Plugin,
	input I,
	terminal WaterfallAction[I, O],
) (O, error) {
	var output O
	if terminal == nil {
		return output, errors.New("plugin: Waterfall terminal is nil")
	}
	sourceFiber, err := activeFiberOf(source)
	if err != nil {
		return output, err
	}
	runtimeEngine := sourceFiber.runtime
	runtimeEngine.view.RLock()
	if sourceFiber.state != FiberActive {
		runtimeEngine.view.RUnlock()
		return output, ErrPluginNotActive
	}
	middleware, err := snapshotWaterfall[I, O](
		runtimeEngine.bindings.waterfalls,
		sourceFiber.scope,
	)
	runtimeEngine.view.RUnlock()
	if err != nil {
		return output, err
	}
	firstStep := &waterfallStep[I, O]{
		middleware: middleware,
		terminal:   terminal,
	}
	return firstStep.Execute(requestContext, input)
}
