package plugin_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gorenx/goren/plugin"
)

type shutdownBlockingMiddleware struct {
	plugin.Base
	started chan struct{}
}

func (owner *shutdownBlockingMiddleware) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "shutdown-blocking-waterfall",
		Waterfalls: []plugin.WaterfallMiddlewareBinding{
			plugin.WaterfallOf[formatInput, formatOutput](owner),
		},
	}
}

func (*shutdownBlockingMiddleware) Apply(context.Context) error {
	return nil
}

func (*shutdownBlockingMiddleware) Dispose(context.Context) error {
	return nil
}

func (owner *shutdownBlockingMiddleware) Intercept(
	requestContext context.Context,
	_ formatInput,
	_ plugin.WaterfallAction[formatInput, formatOutput],
) (formatOutput, error) {
	close(owner.started)
	<-requestContext.Done()
	return formatOutput{}, context.Cause(requestContext)
}

type formatInput struct {
	plugin.WaterfallInputBase
	Value string
}

type formatOutput struct {
	plugin.WaterfallOutputBase
	Value string
}

type formatMiddleware struct {
	plugin.Base
	name  string
	order *[]string
}

func (middleware *formatMiddleware) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: middleware.name,
		Waterfalls: []plugin.WaterfallMiddlewareBinding{
			plugin.WaterfallOf[formatInput, formatOutput](middleware),
		},
	}
}

func (*formatMiddleware) Apply(context.Context) error {
	return nil
}

func (*formatMiddleware) Dispose(context.Context) error {
	return nil
}

func (middleware *formatMiddleware) Intercept(
	requestContext context.Context,
	input formatInput,
	downstream plugin.WaterfallAction[formatInput, formatOutput],
) (formatOutput, error) {
	*middleware.order = append(*middleware.order, middleware.name+":before")
	output, err := downstream.Execute(requestContext, input)
	*middleware.order = append(*middleware.order, middleware.name+":after")
	return output, err
}

type formatAction struct {
	order *[]string
}

func (action formatAction) Execute(
	_ context.Context,
	input formatInput,
) (formatOutput, error) {
	*action.order = append(*action.order, "action")
	return formatOutput{
		Value: input.Value,
	}, nil
}

func TestWaterfallRunsRootToCurrentAsOnion(t *testing.T) {
	t.Parallel()
	order := make([]string, 0)
	rootMiddleware := &formatMiddleware{
		name:  "root",
		order: &order,
	}
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	rootHandles, err := runtimeEngine.Start(context.Background(), rootMiddleware)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	childMiddleware := &formatMiddleware{
		name:  "child",
		order: &order,
	}
	childHandle, err := runtimeEngine.MountScopedChild(
		context.Background(),
		rootHandles[0],
		childMiddleware,
	)
	if err != nil {
		t.Fatalf("mount child Middleware: %v", err)
	}
	source := &eventPublisherPlugin{
		name: "waterfall-source",
	}
	if _, err := runtimeEngine.MountScopedChild(
		context.Background(),
		childHandle,
		source,
	); err != nil {
		t.Fatalf("mount source: %v", err)
	}
	output, err := plugin.Run(
		context.Background(),
		source,
		formatInput{
			Value: "value",
		},
		formatAction{
			order: &order,
		},
	)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if output.Value != "value" {
		t.Fatalf("output = %q", output.Value)
	}
	if got := strings.Join(order, ","); got != "root:before,child:before,action,child:after,root:after" {
		t.Fatalf("waterfall order = %q", got)
	}
}

func TestWaterfallMiddlewareInChildFiberSharesSourceScope(t *testing.T) {
	t.Parallel()
	order := make([]string, 0)
	source := &eventPublisherPlugin{
		name: "same-scope-waterfall-source",
	}
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	handles, err := runtimeEngine.Start(context.Background(), source)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	middleware := &formatMiddleware{
		name:  "same-scope",
		order: &order,
	}
	if _, err := runtimeEngine.MountChild(
		context.Background(),
		handles[0],
		middleware,
	); err != nil {
		t.Fatalf("mount same-Scope Middleware: %v", err)
	}
	if _, err := plugin.Run(
		context.Background(),
		source,
		formatInput{
			Value: "value",
		},
		formatAction{
			order: &order,
		},
	); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := strings.Join(order, ","); got != "same-scope:before,action,same-scope:after" {
		t.Fatalf("waterfall order = %q", got)
	}
}

type doubleExecuteMiddleware struct {
	plugin.Base
	secondErr error
}

func (middleware *doubleExecuteMiddleware) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "double-proceed",
		Waterfalls: []plugin.WaterfallMiddlewareBinding{
			plugin.WaterfallOf[formatInput, formatOutput](middleware),
		},
	}
}

func (*doubleExecuteMiddleware) Apply(context.Context) error {
	return nil
}

func (*doubleExecuteMiddleware) Dispose(context.Context) error {
	return nil
}

func (middleware *doubleExecuteMiddleware) Intercept(
	requestContext context.Context,
	input formatInput,
	downstream plugin.WaterfallAction[formatInput, formatOutput],
) (formatOutput, error) {
	output, err := downstream.Execute(requestContext, input)
	if err != nil {
		return output, err
	}
	_, middleware.secondErr = downstream.Execute(requestContext, input)
	return output, nil
}

func TestWaterfallRejectsSecondExecute(t *testing.T) {
	t.Parallel()
	middleware := &doubleExecuteMiddleware{}
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	if _, err := runtimeEngine.Start(context.Background(), middleware); err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := plugin.Run(
		context.Background(),
		middleware,
		formatInput{},
		formatAction{
			order: &[]string{},
		},
	); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !errors.Is(middleware.secondErr, plugin.ErrWaterfallAlreadyExecuted) {
		t.Fatalf("second Execute error = %v", middleware.secondErr)
	}
}

type panickingWaterfallMiddleware struct {
	plugin.Base
}

func (owner *panickingWaterfallMiddleware) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "panicking-waterfall",
		Waterfalls: []plugin.WaterfallMiddlewareBinding{
			plugin.WaterfallOf[formatInput, formatOutput](
				owner,
			),
		},
	}
}

func (*panickingWaterfallMiddleware) Apply(context.Context) error {
	return nil
}

func (*panickingWaterfallMiddleware) Dispose(context.Context) error {
	return nil
}

func (*panickingWaterfallMiddleware) Intercept(
	context.Context,
	formatInput,
	plugin.WaterfallAction[formatInput, formatOutput],
) (formatOutput, error) {
	panic("broken Middleware")
}

func TestWaterfallContainsMiddlewarePanic(t *testing.T) {
	t.Parallel()
	middleware := &panickingWaterfallMiddleware{}
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	if _, err := runtimeEngine.Start(context.Background(), middleware); err != nil {
		t.Fatalf("start: %v", err)
	}
	_, err := plugin.Run(
		context.Background(),
		middleware,
		formatInput{},
		formatAction{
			order: &[]string{},
		},
	)
	if err == nil || !strings.Contains(err.Error(), "broken Middleware") {
		t.Fatalf("run error = %v", err)
	}
}

func TestShutdownCancelsAdmittedWaterfallBeforeDrainingCalls(t *testing.T) {
	t.Parallel()
	lifecycleMiddleware := &shutdownBlockingMiddleware{
		started: make(chan struct{}),
	}
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	handles, err := runtimeEngine.Start(context.Background(), lifecycleMiddleware)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	source := &eventPublisherPlugin{
		name: "shutdown-waterfall-source",
	}
	if _, err = runtimeEngine.MountScopedChild(
		context.Background(),
		handles[0],
		source,
	); err != nil {
		t.Fatalf("mount source: %v", err)
	}

	runDone := make(chan error, 1)
	go func() {
		_, runErr := plugin.Run(
			context.Background(),
			source,
			formatInput{},
			formatAction{},
		)
		runDone <- runErr
	}()
	select {
	case <-lifecycleMiddleware.started:
	case <-time.After(time.Second):
		t.Fatal("waterfall did not start")
	}

	shutdownDone := make(chan error, 1)
	go func() {
		shutdownDone <- runtimeEngine.Shutdown(context.Background())
	}()
	select {
	case shutdownErr := <-shutdownDone:
		if shutdownErr != nil {
			t.Fatalf("shutdown: %v", shutdownErr)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown did not drain the cancelled waterfall")
	}
	select {
	case runErr := <-runDone:
		if !errors.Is(runErr, plugin.ErrPluginNotActive) {
			t.Fatalf("run error = %v, want ErrPluginNotActive", runErr)
		}
	case <-time.After(time.Second):
		t.Fatal("waterfall did not return after shutdown")
	}
}
