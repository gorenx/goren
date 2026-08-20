package plugin_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gorenx/goren/plugin"
)

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
		Waterfalls: []plugin.WaterfallContribution{
			plugin.WaterfallOf[formatInput, formatOutput](),
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
	childHandle, err := runtimeEngine.MountChild(
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
	if _, err := runtimeEngine.MountChild(
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

type doubleExecuteMiddleware struct {
	plugin.Base
	secondErr error
}

func (*doubleExecuteMiddleware) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "double-proceed",
		Waterfalls: []plugin.WaterfallContribution{
			plugin.WaterfallOf[formatInput, formatOutput](),
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
