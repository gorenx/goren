package example_test

import (
	"context"
	"fmt"
	"strings"

	"github.com/gorenx/goren/plugin"
)

type messageInput struct {
	plugin.WaterfallInputBase
	Text string
}

type messageOutput struct {
	plugin.WaterfallOutputBase
	Text string
}

type trimMiddleware struct {
	plugin.Base
}

func (*trimMiddleware) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "trim",
		Waterfalls: []plugin.WaterfallContribution{
			plugin.WaterfallOf[messageInput, messageOutput](),
		},
	}
}

func (*trimMiddleware) Apply(context.Context) error {
	return nil
}

func (*trimMiddleware) Dispose(context.Context) error {
	return nil
}

func (*trimMiddleware) Intercept(
	requestContext context.Context,
	input messageInput,
	downstream plugin.WaterfallAction[messageInput, messageOutput],
) (messageOutput, error) {
	input.Text = strings.TrimSpace(input.Text)
	return downstream.Execute(requestContext, input)
}

type uppercaseAction struct{}

func (uppercaseAction) Execute(
	_ context.Context,
	input messageInput,
) (messageOutput, error) {
	return messageOutput{
		Text: strings.ToUpper(input.Text),
	}, nil
}

func Example_waterfall() {
	middleware := &trimMiddleware{}
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	if _, err := runtimeEngine.Start(context.Background(), middleware); err != nil {
		panic(err)
	}
	defer runtimeEngine.Shutdown(context.Background())

	output, err := plugin.Run(
		context.Background(),
		middleware,
		messageInput{
			Text: "  goren  ",
		},
		uppercaseAction{},
	)
	if err != nil {
		panic(err)
	}
	fmt.Println(output.Text)
	// Output: GOREN
}
