package plugin

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
)

type runtimeTestService interface {
	Service
	Value() string
}

type runtimeTestServiceObject struct {
	ServiceBase
	value string
}

func (owner *runtimeTestServiceObject) Value() string {
	return owner.value
}

var runtimeTestServiceDefinition = DefineService[runtimeTestService]("test/runtime-service")

type runtimeTestPlugin struct {
	metadata         Manifest
	applyOperation   func(context.Context, *Context) error
	disposeOperation func(context.Context) error
}

func (instance *runtimeTestPlugin) Manifest() Manifest {
	return instance.metadata
}

func (instance *runtimeTestPlugin) Apply(
	applyContext context.Context,
	pluginContext *Context,
) error {
	if instance.applyOperation == nil {
		return nil
	}
	return instance.applyOperation(applyContext, pluginContext)
}

func (instance *runtimeTestPlugin) Dispose(disposeContext context.Context) error {
	if instance.disposeOperation == nil {
		return nil
	}
	return instance.disposeOperation(disposeContext)
}

func TestDefinitionsUseDistinctStableIdentity(t *testing.T) {
	t.Parallel()
	firstService := DefineService[runtimeTestService]("test/same-name")
	secondService := DefineService[runtimeTestService]("test/same-name")
	serviceCopy := firstService
	if firstService.ref.token == secondService.ref.token ||
		firstService.ref.sameDefinition(secondService.ref) {
		t.Fatal("separately defined Services share identity")
	}
	if !firstService.ref.sameDefinition(serviceCopy.ref) {
		t.Fatal("a copied Service Definition lost its identity")
	}

	firstWaterfall := DefineWaterfall[runtimeTestInput, runtimeTestOutput]("test/same-waterfall")
	secondWaterfall := DefineWaterfall[runtimeTestInput, runtimeTestOutput]("test/same-waterfall")
	if firstWaterfall.ref.token == secondWaterfall.ref.token ||
		firstWaterfall.ref.sameDefinition(secondWaterfall.ref) {
		t.Fatal("separately defined Waterfalls share identity")
	}

	firstEvent := DefineEvent[runtimeTestEvent]("test/same-event", DeliveryOrdered)
	secondEvent := DefineEvent[runtimeTestEvent]("test/same-event", DeliveryOrdered)
	if firstEvent.ref.token == secondEvent.ref.token || firstEvent.ref.sameDefinition(secondEvent.ref) {
		t.Fatal("separately defined Events share identity")
	}
}

func TestRuntimeWaitsForRequiredServiceAndReactivatesConsumer(t *testing.T) {
	t.Parallel()
	runtimeEngine := NewRuntime(RuntimeSettings{})
	var applyCount atomic.Int32
	var disposeCount atomic.Int32
	consumer := &runtimeTestPlugin{
		metadata: Manifest{
			Name:     "test-consumer",
			Requires: []ServiceDefinition{runtimeTestServiceDefinition},
		},
		applyOperation: func(_ context.Context, pluginContext *Context) error {
			providedService, err := runtimeTestServiceDefinition.Require(pluginContext)
			if err != nil {
				return err
			}
			if providedService.Value() == "" {
				return errors.New("empty Service value")
			}
			applyCount.Add(1)
			return nil
		},
		disposeOperation: func(context.Context) error {
			disposeCount.Add(1)
			return nil
		},
	}
	consumerHandle, err := runtimeEngine.Load(context.Background(), consumer)
	if err != nil {
		t.Fatal(err)
	}
	consumerStatus, err := runtimeEngine.Status(consumerHandle)
	if err != nil {
		t.Fatal(err)
	}
	if consumerStatus.State != FiberWaiting || applyCount.Load() != 0 || disposeCount.Load() != 0 {
		t.Fatalf("consumer started without dependency: %+v", consumerStatus)
	}

	providerHandle, err := runtimeEngine.Load(
		context.Background(),
		newRuntimeTestProvider("v1"),
	)
	if err != nil {
		t.Fatal(err)
	}
	consumerStatus, err = runtimeEngine.Status(consumerHandle)
	if err != nil {
		t.Fatal(err)
	}
	if consumerStatus.State != FiberActive || applyCount.Load() != 1 ||
		len(consumerStatus.Dependencies) != 1 {
		t.Fatalf("consumer did not activate with its resolved dependency: %+v", consumerStatus)
	}

	if err := runtimeEngine.Unload(context.Background(), providerHandle); err != nil {
		t.Fatal(err)
	}
	consumerStatus, err = runtimeEngine.Status(consumerHandle)
	if err != nil {
		t.Fatal(err)
	}
	if consumerStatus.State != FiberWaiting || disposeCount.Load() != 1 ||
		!reflect.DeepEqual(consumerStatus.Missing, []string{runtimeTestServiceDefinition.Name()}) {
		t.Fatalf("consumer did not return to Waiting: %+v", consumerStatus)
	}

	if _, err := runtimeEngine.Load(context.Background(), newRuntimeTestProvider("v2")); err != nil {
		t.Fatal(err)
	}
	if applyCount.Load() != 2 || disposeCount.Load() != 1 {
		t.Fatalf(
			"consumer lifecycle counts = apply %d, dispose %d; want apply 2, dispose 1",
			applyCount.Load(),
			disposeCount.Load(),
		)
	}
}

func TestServiceProviderMustUseFiberRootScope(t *testing.T) {
	t.Parallel()
	runtimeEngine := NewRuntime(RuntimeSettings{})
	invalidProvider := &runtimeTestPlugin{
		metadata: Manifest{
			Name:     "invalid-provider",
			Provides: []ServiceDefinition{runtimeTestServiceDefinition},
		},
		applyOperation: func(_ context.Context, pluginContext *Context) error {
			childContext, err := pluginContext.ChildScope("child")
			if err != nil {
				return err
			}
			return runtimeTestServiceDefinition.Provide(
				childContext,
				&runtimeTestServiceObject{
					value: "invalid",
				},
			)
		},
	}
	pluginHandle, err := runtimeEngine.Load(context.Background(), invalidProvider)
	if err == nil {
		t.Fatal("provider mounted from an ordinary Child Scope")
	}
	pluginStatus, statusErr := runtimeEngine.Status(pluginHandle)
	if statusErr != nil {
		t.Fatal(statusErr)
	}
	if pluginStatus.State != FiberFailed || len(pluginStatus.Services) != 0 {
		t.Fatalf("invalid provider left visible state: %+v", pluginStatus)
	}
}

func TestRegistrationsCloseAfterApply(t *testing.T) {
	t.Parallel()
	runtimeEngine := NewRuntime(RuntimeSettings{})
	var retainedContext *Context
	pluginHandle, err := runtimeEngine.Load(
		context.Background(),
		&runtimeTestPlugin{
			metadata: Manifest{
				Name: "retained-context",
			},
			applyOperation: func(_ context.Context, pluginContext *Context) error {
				retainedContext = pluginContext
				return nil
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if pluginHandle.ID() == 0 || retainedContext == nil {
		t.Fatal("plugin did not activate")
	}
	registrationErr := runtimeTestEventDefinition.Observe(
		retainedContext,
		&runtimeTestObserver{},
	)
	if !errors.Is(registrationErr, ErrRegistrationClosed) {
		t.Fatalf("registration error = %v, want %v", registrationErr, ErrRegistrationClosed)
	}
	if _, childErr := retainedContext.Mount(
		context.Background(),
		&runtimeTestPlugin{
			metadata: Manifest{
				Name: "active-child",
			},
		},
	); childErr != nil {
		t.Fatalf("active parent could not load child: %v", childErr)
	}
}

func TestFiberEffectsReleaseInReverseOrder(t *testing.T) {
	t.Parallel()
	trace := make([]string, 0)
	entries := []*fiberEffect{
		{
			label: "first",
			release: func(context.Context) error {
				trace = append(trace, "first")
				return nil
			},
			state: fiberEffectActive,
		},
		{
			label: "second",
			release: func(context.Context) error {
				trace = append(trace, "second")
				return nil
			},
			state: fiberEffectActive,
		},
	}
	if err := releaseFiberEffects(context.Background(), entries); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(trace, []string{"second", "first"}) {
		t.Fatalf("effect release trace = %v", trace)
	}
	if err := releaseFiberEffects(context.Background(), entries); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(trace, []string{"second", "first"}) {
		t.Fatalf("effect release was not idempotent: %v", trace)
	}
}

func TestApplyFailureDisposesPartiallyStartedPlugin(t *testing.T) {
	t.Parallel()
	runtimeEngine := NewRuntime(RuntimeSettings{})
	trace := make([]string, 0, 2)
	pluginHandle, err := runtimeEngine.Load(
		context.Background(),
		&runtimeTestPlugin{
			metadata: Manifest{
				Name: "partial-startup",
			},
			applyOperation: func(context.Context, *Context) error {
				trace = append(trace, "apply")
				return errors.New("startup failed")
			},
			disposeOperation: func(context.Context) error {
				trace = append(trace, "dispose")
				return nil
			},
		},
	)
	if err == nil {
		t.Fatal("failed Apply unexpectedly activated")
	}
	if !reflect.DeepEqual(
		trace,
		[]string{"apply", "dispose"},
	) {
		t.Fatalf("plugin lifecycle trace = %v", trace)
	}
	pluginStatus, statusErr := runtimeEngine.Status(pluginHandle)
	if statusErr != nil {
		t.Fatal(statusErr)
	}
	if pluginStatus.State != FiberFailed || len(pluginStatus.Effects) != 0 {
		t.Fatalf("failed activation retained effects: %+v", pluginStatus)
	}
}

func TestPluginDisposeRunsAfterRegistrationsAreWithdrawn(t *testing.T) {
	t.Parallel()
	runtimeEngine := NewRuntime(RuntimeSettings{})
	var ownerContext *Context
	registrationsWithdrawn := false
	pluginHandle, err := runtimeEngine.Load(
		context.Background(),
		&runtimeTestPlugin{
			metadata: Manifest{
				Name: "dispose-order",
				Provides: []ServiceDefinition{
					runtimeTestServiceDefinition,
				},
			},
			applyOperation: func(_ context.Context, pluginContext *Context) error {
				ownerContext = pluginContext
				return runtimeTestServiceDefinition.Provide(
					pluginContext,
					&runtimeTestServiceObject{
						value: "active",
					},
				)
			},
			disposeOperation: func(context.Context) error {
				for _, ownedEffect := range ownerContext.ownerFiber.effects.entries {
					if ownedEffect.registration != nil &&
						ownedEffect.state == fiberEffectActive {
						return errors.New("Plugin.Dispose ran before registration withdrawal")
					}
				}
				registrationsWithdrawn = true
				return nil
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := pluginHandle.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !registrationsWithdrawn {
		t.Fatal("Plugin.Dispose was not called")
	}
}

func TestRegistrationsShareFiberEffectOwnership(t *testing.T) {
	t.Parallel()
	runtimeEngine := NewRuntime(RuntimeSettings{})
	pluginHandle, err := runtimeEngine.Load(
		context.Background(),
		&runtimeTestPlugin{
			metadata: Manifest{
				Name: "owned-registrations",
				Provides: []ServiceDefinition{
					runtimeTestServiceDefinition,
				},
			},
			applyOperation: func(_ context.Context, pluginContext *Context) error {
				if provideErr := runtimeTestServiceDefinition.Provide(
					pluginContext,
					&runtimeTestServiceObject{
						value: "owned",
					},
				); provideErr != nil {
					return provideErr
				}
				if useErr := runtimeTestWaterfallDefinition.Use(
					pluginContext,
					&runtimeTestMiddleware{
						name:  "owned",
						trace: &[]string{},
					},
				); useErr != nil {
					return useErr
				}
				return runtimeTestEventDefinition.Observe(
					pluginContext,
					&runtimeTestObserver{},
				)
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	statusView, err := runtimeEngine.Status(pluginHandle)
	if err != nil {
		t.Fatal(err)
	}
	expectedLabels := []string{
		"plugin:owned-registrations",
		"provide:test/runtime-service",
		"waterfall:test/waterfall",
		"observe:test/event",
	}
	if !reflect.DeepEqual(statusView.Effects, expectedLabels) ||
		len(statusView.Services) != 1 || len(statusView.Waterfalls) != 1 || len(statusView.Events) != 1 {
		t.Fatalf("registrations do not share Fiber effect ownership: %+v", statusView)
	}
	if err := pluginHandle.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	statusView, err = runtimeEngine.Status(pluginHandle)
	if err != nil {
		t.Fatal(err)
	}
	if statusView.State != FiberStopped || len(statusView.Effects) != 0 {
		t.Fatalf("stopped Fiber retained effects: %+v", statusView)
	}
}

func TestMountedPluginIsOwnedByParentFiberEffect(t *testing.T) {
	t.Parallel()
	runtimeEngine := NewRuntime(RuntimeSettings{})
	var retainedContext *Context
	parentHandle, err := runtimeEngine.Load(
		context.Background(),
		&runtimeTestPlugin{
			metadata: Manifest{
				Name: "parent-effect-owner",
			},
			applyOperation: func(_ context.Context, pluginContext *Context) error {
				retainedContext = pluginContext
				return nil
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	childHandle, err := retainedContext.Mount(
		context.Background(),
		&runtimeTestPlugin{
			metadata: Manifest{
				Name: "owned-child",
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	parentStatus, err := runtimeEngine.Status(parentHandle)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(
		parentStatus.Effects,
		[]string{"plugin:parent-effect-owner", "plugin:owned-child"},
	) {
		t.Fatalf("parent does not own Child Plugin as an Effect: %+v", parentStatus)
	}
	if err := parentHandle.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	childStatus, err := runtimeEngine.Status(childHandle)
	if err != nil {
		t.Fatal(err)
	}
	if childStatus.State != FiberStopped {
		t.Fatalf("Child Plugin outlived parent Fiber: %+v", childStatus)
	}
}

func TestPluginCanMountChildDuringApply(t *testing.T) {
	t.Parallel()
	runtimeEngine := NewRuntime(RuntimeSettings{})
	trace := make([]string, 0, 5)
	var childHandle Handle
	parentHandle, err := runtimeEngine.Load(
		context.Background(),
		&runtimeTestPlugin{
			metadata: Manifest{
				Name: "apply-mount-parent",
			},
			applyOperation: func(
				applyContext context.Context,
				pluginContext *Context,
			) error {
				trace = append(trace, "parent-apply")
				var mountErr error
				childHandle, mountErr = pluginContext.Mount(
					applyContext,
					&runtimeTestPlugin{
						metadata: Manifest{
							Name: "apply-mounted-child",
						},
						applyOperation: func(context.Context, *Context) error {
							trace = append(trace, "child-apply")
							return nil
						},
						disposeOperation: func(context.Context) error {
							trace = append(trace, "child-dispose")
							return nil
						},
					},
				)
				trace = append(trace, "parent-applied")
				return mountErr
			},
			disposeOperation: func(context.Context) error {
				trace = append(trace, "parent-dispose")
				return nil
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(
		trace,
		[]string{"parent-apply", "child-apply", "parent-applied"},
	) {
		t.Fatalf("Apply mount trace = %v", trace)
	}
	childStatus, err := runtimeEngine.Status(childHandle)
	if err != nil {
		t.Fatal(err)
	}
	if childStatus.State != FiberActive {
		t.Fatalf("mounted child did not activate: %+v", childStatus)
	}
	if err := parentHandle.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(
		trace,
		[]string{
			"parent-apply",
			"child-apply",
			"parent-applied",
			"child-dispose",
			"parent-dispose",
		},
	) {
		t.Fatalf("mounted lifecycle trace = %v", trace)
	}
}

func TestMountedChildWaitsForParentServiceCommit(t *testing.T) {
	t.Parallel()
	runtimeEngine := NewRuntime(RuntimeSettings{})
	observedValue := ""
	var childHandle Handle
	_, err := runtimeEngine.Load(
		context.Background(),
		&runtimeTestPlugin{
			metadata: Manifest{
				Name: "staged-service-parent",
				Provides: []ServiceDefinition{
					runtimeTestServiceDefinition,
				},
			},
			applyOperation: func(
				applyContext context.Context,
				pluginContext *Context,
			) error {
				if provideErr := runtimeTestServiceDefinition.Provide(
					pluginContext,
					&runtimeTestServiceObject{
						value: "parent",
					},
				); provideErr != nil {
					return provideErr
				}
				var mountErr error
				childHandle, mountErr = pluginContext.Mount(
					applyContext,
					&runtimeTestPlugin{
						metadata: Manifest{
							Name: "staged-service-child",
							Requires: []ServiceDefinition{
								runtimeTestServiceDefinition,
							},
						},
						applyOperation: func(_ context.Context, childContext *Context) error {
							providedService, requireErr := runtimeTestServiceDefinition.Require(childContext)
							if requireErr != nil {
								return requireErr
							}
							observedValue = providedService.Value()
							return nil
						},
					},
				)
				return mountErr
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	childStatus, err := runtimeEngine.Status(childHandle)
	if err != nil {
		t.Fatal(err)
	}
	if childStatus.State != FiberActive || observedValue != "parent" {
		t.Fatalf(
			"child did not settle after parent Service commit: status=%+v value=%q",
			childStatus,
			observedValue,
		)
	}
}

func TestIgnoredMountFailureRollsBackParentApply(t *testing.T) {
	t.Parallel()
	runtimeEngine := NewRuntime(RuntimeSettings{})
	trace := make([]string, 0, 3)
	var childHandle Handle
	parentHandle, err := runtimeEngine.Load(
		context.Background(),
		&runtimeTestPlugin{
			metadata: Manifest{
				Name: "failed-mount-parent",
			},
			applyOperation: func(
				applyContext context.Context,
				pluginContext *Context,
			) error {
				childHandle, _ = pluginContext.Mount(
					applyContext,
					&runtimeTestPlugin{
						metadata: Manifest{
							Name: "failed-mounted-child",
						},
						applyOperation: func(context.Context, *Context) error {
							trace = append(trace, "child-apply")
							return errors.New("child startup failed")
						},
						disposeOperation: func(context.Context) error {
							trace = append(trace, "child-dispose")
							return nil
						},
					},
				)
				return nil
			},
			disposeOperation: func(context.Context) error {
				trace = append(trace, "parent-dispose")
				return nil
			},
		},
	)
	if err == nil {
		t.Fatal("ignored child startup failure committed the parent")
	}
	parentStatus, statusErr := runtimeEngine.Status(parentHandle)
	if statusErr != nil {
		t.Fatal(statusErr)
	}
	childStatus, statusErr := runtimeEngine.Status(childHandle)
	if statusErr != nil {
		t.Fatal(statusErr)
	}
	if parentStatus.State != FiberFailed || childStatus.State != FiberStopped {
		t.Fatalf("failed mount states: parent=%+v child=%+v", parentStatus, childStatus)
	}
	if !reflect.DeepEqual(
		trace,
		[]string{"child-apply", "child-dispose", "parent-dispose"},
	) {
		t.Fatalf("failed mount rollback trace = %v", trace)
	}
}

func TestFailedReplacementKeepsLastKnownGoodFiber(t *testing.T) {
	t.Parallel()
	runtimeEngine := NewRuntime(RuntimeSettings{})
	providerHandle, err := runtimeEngine.Load(
		context.Background(),
		newRuntimeTestProvider("v1"),
	)
	if err != nil {
		t.Fatal(err)
	}
	before, err := runtimeEngine.Status(providerHandle)
	if err != nil {
		t.Fatal(err)
	}
	replacementErr := runtimeEngine.Replace(
		context.Background(),
		providerHandle,
		&runtimeTestPlugin{
			metadata: Manifest{
				Name:     "test-provider",
				Provides: []ServiceDefinition{runtimeTestServiceDefinition},
			},
			applyOperation: func(context.Context, *Context) error {
				return errors.New("candidate failed")
			},
		},
	)
	if replacementErr == nil {
		t.Fatal("failed replacement returned nil error")
	}
	after, err := runtimeEngine.Status(providerHandle)
	if err != nil {
		t.Fatal(err)
	}
	if after.State != FiberActive || after.FiberID != before.FiberID || len(after.Services) != 1 {
		t.Fatalf("last-known-good Fiber was not preserved: before=%+v after=%+v", before, after)
	}
}

func TestReplacementCanMountChildDuringApply(t *testing.T) {
	t.Parallel()
	runtimeEngine := NewRuntime(RuntimeSettings{})
	parentHandle, err := runtimeEngine.Load(
		context.Background(),
		&runtimeTestPlugin{
			metadata: Manifest{
				Name: "replacement-mount-parent",
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	var childHandle Handle
	replacementErr := runtimeEngine.Replace(
		context.Background(),
		parentHandle,
		&runtimeTestPlugin{
			metadata: Manifest{
				Name: "replacement-mount-parent",
			},
			applyOperation: func(
				applyContext context.Context,
				pluginContext *Context,
			) error {
				var mountErr error
				childHandle, mountErr = pluginContext.Mount(
					applyContext,
					&runtimeTestPlugin{
						metadata: Manifest{
							Name: "replacement-mounted-child",
						},
					},
				)
				return mountErr
			},
		},
	)
	if replacementErr != nil {
		t.Fatal(replacementErr)
	}
	childStatus, err := runtimeEngine.Status(childHandle)
	if err != nil {
		t.Fatal(err)
	}
	if childStatus.State != FiberActive {
		t.Fatalf("replacement child did not activate: %+v", childStatus)
	}
	if err := parentHandle.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	childStatus, err = runtimeEngine.Status(childHandle)
	if err != nil {
		t.Fatal(err)
	}
	if childStatus.State != FiberStopped {
		t.Fatalf("replacement child outlived parent: %+v", childStatus)
	}
}

func TestSuccessfulReplacementReactivatesResolvedConsumers(t *testing.T) {
	t.Parallel()
	runtimeEngine := NewRuntime(RuntimeSettings{})
	providerHandle, err := runtimeEngine.Load(
		context.Background(),
		newRuntimeTestProvider("v1"),
	)
	if err != nil {
		t.Fatal(err)
	}
	values := make([]string, 0)
	consumerHandle, err := runtimeEngine.Load(
		context.Background(),
		&runtimeTestPlugin{
			metadata: Manifest{
				Name:     "replacement-consumer",
				Requires: []ServiceDefinition{runtimeTestServiceDefinition},
			},
			applyOperation: func(_ context.Context, pluginContext *Context) error {
				providedService, requireErr := runtimeTestServiceDefinition.Require(pluginContext)
				if requireErr != nil {
					return requireErr
				}
				values = append(values, providedService.Value())
				return nil
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	before, err := runtimeEngine.Status(providerHandle)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtimeEngine.Replace(
		context.Background(),
		providerHandle,
		newRuntimeTestProvider("v2"),
	); err != nil {
		t.Fatal(err)
	}
	after, err := runtimeEngine.Status(providerHandle)
	if err != nil {
		t.Fatal(err)
	}
	consumerStatus, err := runtimeEngine.Status(consumerHandle)
	if err != nil {
		t.Fatal(err)
	}
	if before.FiberID == after.FiberID || after.State != FiberActive ||
		consumerStatus.State != FiberActive || !reflect.DeepEqual(values, []string{"v1", "v2"}) {
		t.Fatalf(
			"replacement did not atomically rebind: before=%+v after=%+v consumer=%+v values=%v",
			before,
			after,
			consumerStatus,
			values,
		)
	}
}

func newRuntimeTestProvider(serviceValue string) *runtimeTestPlugin {
	return &runtimeTestPlugin{
		metadata: Manifest{
			Name:     "test-provider",
			Provides: []ServiceDefinition{runtimeTestServiceDefinition},
		},
		applyOperation: func(_ context.Context, pluginContext *Context) error {
			return runtimeTestServiceDefinition.Provide(
				pluginContext,
				&runtimeTestServiceObject{
					value: serviceValue,
				},
			)
		},
	}
}
