package plugin_test

import (
	"context"
	"errors"
	"testing"

	"github.com/gorenx/goren/plugin"
)

type topologyInput struct {
	plugin.WaterfallInputBase
}

type topologyOutput struct {
	plugin.WaterfallOutputBase
}

type nestedTopologyInput struct {
	plugin.WaterfallInputBase
}

type nestedTopologyOutput struct {
	plugin.WaterfallOutputBase
}

type topologyPlugin struct {
	plugin.Base
	name string
}

func (owner *topologyPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: owner.name,
	}
}

func (*topologyPlugin) Apply(requestContext context.Context) error {
	return requestContext.Err()
}

func (*topologyPlugin) Dispose(context.Context) error {
	return nil
}

type topologyAction struct {
	owner plugin.Plugin
}

func (action topologyAction) Execute(
	requestContext context.Context,
	_ topologyInput,
) (topologyOutput, error) {
	_, mountErr := plugin.MountScopedChild(
		requestContext,
		action.owner,
		&topologyPlugin{
			name: "waterfall-terminal-child",
		},
	)
	return topologyOutput{}, mountErr
}

func TestWaterfallTerminalKeepsOrdinaryBusinessTopologyAccess(t *testing.T) {
	source := &topologyPlugin{
		name: "waterfall-topology-source",
	}
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	if _, startErr := runtimeEngine.Start(context.Background(), source); startErr != nil {
		t.Fatal(startErr)
	}
	t.Cleanup(func() {
		if shutdownErr := runtimeEngine.Shutdown(context.Background()); shutdownErr != nil {
			t.Error(shutdownErr)
		}
	})
	if _, runErr := plugin.Run(
		context.Background(),
		source,
		topologyInput{},
		topologyAction{
			owner: source,
		},
	); runErr != nil {
		t.Fatal(runErr)
	}
}

type nestedTopologyAction struct {
	owner plugin.Plugin
}

func (action nestedTopologyAction) Execute(
	requestContext context.Context,
	_ nestedTopologyInput,
) (nestedTopologyOutput, error) {
	_, mountErr := plugin.MountScopedChild(
		requestContext,
		action.owner,
		&topologyPlugin{
			name: "waterfall-nested-terminal-child",
		},
	)
	return nestedTopologyOutput{}, mountErr
}

type topologyMiddleware struct {
	plugin.Base
	nestedSource plugin.Plugin
	nestedErr    error
}

func (owner *topologyMiddleware) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "waterfall-topology-middleware",
		Waterfalls: []plugin.WaterfallMiddlewareBinding{
			plugin.WaterfallOf[topologyInput, topologyOutput](owner),
		},
	}
}

func (*topologyMiddleware) Apply(requestContext context.Context) error {
	return requestContext.Err()
}

func (*topologyMiddleware) Dispose(context.Context) error {
	return nil
}

func (owner *topologyMiddleware) Intercept(
	requestContext context.Context,
	input topologyInput,
	downstream plugin.WaterfallAction[topologyInput, topologyOutput],
) (topologyOutput, error) {
	_, owner.nestedErr = plugin.Run(
		requestContext,
		owner.nestedSource,
		nestedTopologyInput{},
		nestedTopologyAction{
			owner: owner.nestedSource,
		},
	)
	return downstream.Execute(requestContext, input)
}

type emptyTopologyAction struct{}

func (emptyTopologyAction) Execute(
	context.Context,
	topologyInput,
) (topologyOutput, error) {
	return topologyOutput{}, nil
}

func TestWaterfallTerminalCannotEscapeExistingCallbackGuard(t *testing.T) {
	source := &topologyPlugin{
		name: "waterfall-guarded-source",
	}
	middleware := &topologyMiddleware{
		nestedSource: source,
	}
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	if _, startErr := runtimeEngine.Start(
		context.Background(),
		source,
		middleware,
	); startErr != nil {
		t.Fatal(startErr)
	}
	t.Cleanup(func() {
		if shutdownErr := runtimeEngine.Shutdown(context.Background()); shutdownErr != nil {
			t.Error(shutdownErr)
		}
	})
	if _, runErr := plugin.Run(
		context.Background(),
		source,
		topologyInput{},
		emptyTopologyAction{},
	); runErr != nil {
		t.Fatal(runErr)
	}
	if !errors.Is(middleware.nestedErr, plugin.ErrTopologyMutation) {
		t.Fatalf("nested callback mutation error = %v", middleware.nestedErr)
	}
}
