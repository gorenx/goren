package plugin

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"sync/atomic"
)

// WaterfallInput marks an owner-defined interceptable operation input.
type WaterfallInput interface {
	RuntimeWaterfallInput()
}

// WaterfallInputBase supplies the WaterfallInput marker by embedding.
type WaterfallInputBase struct{}

// RuntimeWaterfallInput implements WaterfallInput.
func (WaterfallInputBase) RuntimeWaterfallInput() {}

// WaterfallOutput marks an owner-defined interceptable operation output.
type WaterfallOutput interface {
	RuntimeWaterfallOutput()
}

// WaterfallOutputBase supplies the WaterfallOutput marker by embedding.
type WaterfallOutputBase struct{}

// RuntimeWaterfallOutput implements WaterfallOutput.
func (WaterfallOutputBase) RuntimeWaterfallOutput() {}

// WaterfallMiddlewareBinding declares one Middleware owned by the Plugin for
// one typed operation.
type WaterfallMiddlewareBinding interface {
	Name() string
	waterfallReference() (waterfallRef, error)
	waterfallMiddleware() (waterfallInvoker, error)
}

type waterfallRef struct {
	input  reflect.Type
	output reflect.Type
	name   string
}

type waterfallMiddlewareSpec struct {
	reference waterfallRef
	invoker   waterfallInvoker
}

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

type waterfallInvocation[I WaterfallInput, O WaterfallOutput] struct {
	middleware WaterfallMiddleware[I, O]
	owner      *fiber
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
	candidates := registry.bindings[selectedKey]
	insertIndex := sort.Search(len(candidates), func(candidateIndex int) bool {
		return candidates[candidateIndex].ordinal >= binding.ordinal
	})
	candidates = append(candidates, nil)
	copy(candidates[insertIndex+1:], candidates[insertIndex:])
	candidates[insertIndex] = binding
	registry.bindings[selectedKey] = candidates
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
) ([]waterfallInvocation[I, O], error) {
	selectedKey := waterfallKey{
		input:  reflect.TypeFor[I](),
		output: reflect.TypeFor[O](),
	}
	candidates := registry.bindings[selectedKey]
	middleware := make([]waterfallInvocation[I, O], 0, len(candidates))
	return appendWaterfallScope(middleware, candidates, sourceScope)
}

func appendWaterfallScope[
	I WaterfallInput,
	O WaterfallOutput,
](
	middleware []waterfallInvocation[I, O],
	candidates []*waterfallBinding,
	selectedScope *scope,
) ([]waterfallInvocation[I, O], error) {
	if selectedScope == nil {
		return middleware, nil
	}
	var err error
	middleware, err = appendWaterfallScope(middleware, candidates, selectedScope.parent)
	if err != nil {
		return nil, err
	}
	for _, binding := range candidates {
		if binding.scope != selectedScope || binding.owner == nil ||
			binding.owner.state != FiberActive {
			continue
		}
		typedInvoker, matches := binding.invoker.(waterfallInvokerOf[I, O])
		if !matches {
			return nil, errors.New("plugin: Waterfall has an incompatible Middleware")
		}
		middleware = append(middleware, waterfallInvocation[I, O]{
			middleware: typedInvoker.middleware,
			owner:      binding.owner,
		})
	}
	return middleware, nil
}

type waterfallStep[I WaterfallInput, O WaterfallOutput] struct {
	middleware []waterfallInvocation[I, O]
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
		step.middleware[step.index].middleware,
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
	output, lease, err := RunRetained(
		requestContext,
		source,
		input,
		terminal,
	)
	if lease != nil {
		lease.Release()
	}
	return output, err
}

// RunRetained executes the visible onion chain and transfers the admitted
// invocation to the caller. It is for outputs such as pull streams whose work
// continues after the Waterfall method returns. The caller must bind the
// returned lease to that output's terminal and cancellation lifecycle.
func RunRetained[
	I WaterfallInput,
	O WaterfallOutput,
](
	requestContext context.Context,
	source Plugin,
	input I,
	terminal WaterfallAction[I, O],
) (output O, lease *InvocationLease, runErr error) {
	if terminal == nil {
		return output, nil, errors.New("plugin: Waterfall terminal is nil")
	}
	sourceFiber, err := activeFiberOf(source)
	if err != nil {
		return output, nil, err
	}
	runtimeEngine := sourceFiber.runtime
	runtimeEngine.view.RLock()
	if sourceFiber.state != FiberActive {
		runtimeEngine.view.RUnlock()
		return output, nil, ErrPluginNotActive
	}
	middleware, err := snapshotWaterfall[I, O](
		runtimeEngine.bindings.waterfalls,
		sourceFiber.scope,
	)
	if err != nil {
		runtimeEngine.view.RUnlock()
		return output, nil, err
	}
	participants := make([]*fiber, 0, len(middleware)+1)
	participants = append(participants, sourceFiber)
	for _, invocation := range middleware {
		participants = append(participants, invocation.owner)
	}
	callContext, releaseContext := runtimeEngine.invocationContext(
		requestContext,
	)
	admittedCall := &fiberCall{
		cancel: releaseContext,
	}
	releaseCalls, admitted := acquireFiberCalls(admittedCall, participants...)
	runtimeEngine.view.RUnlock()
	if !admitted {
		releaseContext(context.Canceled)
		return output, nil, ErrPluginNotActive
	}
	lease = newInvocationLease(
		callContext,
		releaseContext,
		releaseCalls,
	)
	defer func() {
		if recovered := recover(); recovered != nil {
			lease.Release()
			panic(recovered)
		}
		if runErr != nil {
			lease.Release()
			lease = nil
		}
	}()
	firstStep := &waterfallStep[I, O]{
		middleware: middleware,
		terminal:   terminal,
	}
	output, runErr = firstStep.Execute(
		callContext,
		input,
	)
	return output, lease, runErr
}
