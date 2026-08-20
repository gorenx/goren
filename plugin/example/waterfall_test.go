package example_test

import (
	"context"
	"fmt"

	"github.com/gorenx/goren/plugin"
)

type formatInput struct {
	plugin.WaterfallInputBase
	Text string
}

type formatOutput struct {
	plugin.WaterfallOutputBase
	Text string
}

var formatWaterfallDefinition = plugin.DefineWaterfall[formatInput, formatOutput](
	"example/format",
)

type formatMiddleware struct{}

func (*formatMiddleware) Intercept(
	requestContext context.Context,
	input formatInput,
	next plugin.WaterfallNext[formatInput, formatOutput],
) (formatOutput, error) {
	input.Text = "[" + input.Text + "]"
	return next.Proceed(requestContext, input)
}

type formatTerminal struct{}

func (*formatTerminal) Execute(
	_ context.Context,
	input formatInput,
) (formatOutput, error) {
	return formatOutput{
		Text: input.Text,
	}, nil
}

type waterfallCaller struct {
	sourceScope *plugin.Scope
}

func (caller *waterfallCaller) Run(
	requestContext context.Context,
	text string,
) (string, error) {
	output, err := formatWaterfallDefinition.Run(
		requestContext,
		caller.sourceScope,
		formatInput{
			Text: text,
		},
		&formatTerminal{},
	)
	return output.Text, err
}

type waterfallPlugin struct {
	callerObject *waterfallCaller
}

func (*waterfallPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "example-waterfall",
	}
}

func (instance *waterfallPlugin) Apply(
	_ context.Context,
	pluginContext *plugin.Context,
) error {
	instance.callerObject.sourceScope = pluginContext.Scope()
	return formatWaterfallDefinition.Use(pluginContext, &formatMiddleware{})
}

func (instance *waterfallPlugin) Dispose(context.Context) error {
	instance.callerObject.sourceScope = nil
	return nil
}

func Example_waterfall() {
	callerObject := &waterfallCaller{}
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	if _, err := runtimeEngine.Load(
		context.Background(),
		&waterfallPlugin{
			callerObject: callerObject,
		},
	); err != nil {
		panic(err)
	}

	formattedText, err := callerObject.Run(context.Background(), "hello")
	if err != nil {
		panic(err)
	}
	fmt.Println(formattedText)
	if err := runtimeEngine.Shutdown(context.Background()); err != nil {
		panic(err)
	}

	// Output:
	// [hello]
}
