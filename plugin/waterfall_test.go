package plugin

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type runtimeTestInput struct {
	WaterfallInputBase
	Value string
}

type runtimeTestOutput struct {
	WaterfallOutputBase
	Value string
}

var runtimeTestWaterfallDefinition = DefineWaterfall[runtimeTestInput, runtimeTestOutput](
	"test/waterfall",
)

type runtimeTestMiddleware struct {
	name  string
	trace *[]string
}

func (middleware *runtimeTestMiddleware) Intercept(
	requestContext context.Context,
	input runtimeTestInput,
	next WaterfallNext[runtimeTestInput, runtimeTestOutput],
) (runtimeTestOutput, error) {
	*middleware.trace = append(*middleware.trace, middleware.name+":before")
	output, err := next.Proceed(requestContext, input)
	*middleware.trace = append(*middleware.trace, middleware.name+":after")
	return output, err
}

type runtimeTestTerminal struct {
	trace *[]string
}

func (terminal *runtimeTestTerminal) Execute(
	_ context.Context,
	input runtimeTestInput,
) (runtimeTestOutput, error) {
	*terminal.trace = append(*terminal.trace, "terminal")
	return runtimeTestOutput{
		Value: input.Value,
	}, nil
}

type runtimeDoubleProceedMiddleware struct{}

func (*runtimeDoubleProceedMiddleware) Intercept(
	requestContext context.Context,
	input runtimeTestInput,
	next WaterfallNext[runtimeTestInput, runtimeTestOutput],
) (runtimeTestOutput, error) {
	if _, err := next.Proceed(requestContext, input); err != nil {
		return runtimeTestOutput{}, err
	}
	return next.Proceed(requestContext, input)
}

func TestWaterfallRunsRootToCurrentAsOneShotOnion(t *testing.T) {
	t.Parallel()
	runtimeEngine := NewRuntime(RuntimeSettings{})
	trace := make([]string, 0)
	var sourceScope *Scope
	if _, err := runtimeEngine.Load(
		context.Background(),
		&runtimeTestPlugin{
			metadata: Manifest{
				Name: "scoped-waterfall",
			},
			applyOperation: func(_ context.Context, pluginContext *Context) error {
				if useErr := runtimeTestWaterfallDefinition.Use(
					pluginContext,
					&runtimeTestMiddleware{
						name:  "outer",
						trace: &trace,
					},
				); useErr != nil {
					return useErr
				}
				childContext, childErr := pluginContext.ChildScope("request")
				if childErr != nil {
					return childErr
				}
				sourceScope = childContext.Scope()
				return runtimeTestWaterfallDefinition.Use(
					childContext,
					&runtimeTestMiddleware{
						name:  "inner",
						trace: &trace,
					},
				)
			},
		},
	); err != nil {
		t.Fatal(err)
	}
	output, err := runtimeTestWaterfallDefinition.Run(
		context.Background(),
		sourceScope,
		runtimeTestInput{
			Value: "result",
		},
		&runtimeTestTerminal{
			trace: &trace,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if output.Value != "result" || !reflect.DeepEqual(
		trace,
		[]string{"outer:before", "inner:before", "terminal", "inner:after", "outer:after"},
	) {
		t.Fatalf("output=%+v trace=%v", output, trace)
	}
}

func TestWaterfallRejectsSecondProceed(t *testing.T) {
	t.Parallel()
	runtimeEngine := NewRuntime(RuntimeSettings{})
	var sourceScope *Scope
	if _, err := runtimeEngine.Load(
		context.Background(),
		&runtimeTestPlugin{
			metadata: Manifest{
				Name: "double-proceed",
			},
			applyOperation: func(_ context.Context, pluginContext *Context) error {
				sourceScope = pluginContext.Scope()
				return runtimeTestWaterfallDefinition.Use(
					pluginContext,
					&runtimeDoubleProceedMiddleware{},
				)
			},
		},
	); err != nil {
		t.Fatal(err)
	}
	_, err := runtimeTestWaterfallDefinition.Run(
		context.Background(),
		sourceScope,
		runtimeTestInput{},
		&runtimeTestTerminal{
			trace: &[]string{},
		},
	)
	if !errors.Is(err, ErrWaterfallAlreadyProceeded) {
		t.Fatalf("Run error = %v, want %v", err, ErrWaterfallAlreadyProceeded)
	}
}
