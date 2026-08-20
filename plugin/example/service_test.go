package example_test

import (
	"context"
	"fmt"

	"github.com/gorenx/goren/plugin"
)

type clockService interface {
	plugin.Service
	Now() string
}

// namedClockService demonstrates Go interface embedding. It extends the
// clockService contract without introducing a class hierarchy.
type namedClockService interface {
	clockService
	Name() string
}

var clockServiceDefinition = plugin.DefineService[namedClockService]("example/clock")

type fixedClock struct {
	fixedTime string
}

func (clock fixedClock) Now() string {
	return clock.fixedTime
}

// clockServiceObject uses struct embedding for implementation reuse and embeds
// ServiceBase to satisfy plugin.Service. Go embedding is composition, not
// implementation inheritance.
type clockServiceObject struct {
	plugin.ServiceBase
	fixedClock
	name string
}

func (owner *clockServiceObject) Name() string {
	return owner.name
}

type clockProviderPlugin struct{}

func (*clockProviderPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "example-clock-provider",
		Provides: []plugin.ServiceDefinition{
			clockServiceDefinition,
		},
	}
}

func (*clockProviderPlugin) Apply(
	_ context.Context,
	pluginContext *plugin.Context,
) error {
	return clockServiceDefinition.Provide(
		pluginContext,
		&clockServiceObject{
			fixedClock: fixedClock{
				fixedTime: "10:00",
			},
			name: "primary",
		},
	)
}

func (*clockProviderPlugin) Dispose(context.Context) error {
	return nil
}

type clockConsumerPlugin struct {
	valueOutput *string
}

func (*clockConsumerPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "example-clock-consumer",
		Requires: []plugin.ServiceDefinition{
			clockServiceDefinition,
		},
	}
}

func (instance *clockConsumerPlugin) Apply(
	_ context.Context,
	pluginContext *plugin.Context,
) error {
	providedClock, err := clockServiceDefinition.Require(pluginContext)
	if err != nil {
		return err
	}
	*instance.valueOutput = providedClock.Name() + "=" + providedClock.Now()
	return nil
}

func (*clockConsumerPlugin) Dispose(context.Context) error {
	return nil
}

func Example_service() {
	observedValue := ""
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	if _, err := runtimeEngine.Load(context.Background(), &clockProviderPlugin{}); err != nil {
		panic(err)
	}
	if _, err := runtimeEngine.Load(
		context.Background(),
		&clockConsumerPlugin{
			valueOutput: &observedValue,
		},
	); err != nil {
		panic(err)
	}

	fmt.Println(observedValue)
	if err := runtimeEngine.Shutdown(context.Background()); err != nil {
		panic(err)
	}

	// Output:
	// primary=10:00
}
