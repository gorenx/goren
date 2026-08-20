package example_test

import (
	"context"
	"fmt"
	"sync"

	"github.com/gorenx/goren/plugin"
)

type counterAdvanced struct {
	plugin.EventBase
	Value int
}

var counterAdvancedEvent = plugin.DefineEvent[counterAdvanced](
	"example/counter-advanced",
	plugin.DeliveryOrdered,
)

type counterService interface {
	plugin.Service
	Advance(requestContext context.Context) (int, error)
}

var counterServiceDefinition = plugin.DefineService[counterService]("example/counter")

type counterServiceObject struct {
	plugin.ServiceBase
	mutex       sync.Mutex
	value       int
	sourceScope *plugin.Scope
}

func (owner *counterServiceObject) Advance(
	requestContext context.Context,
) (int, error) {
	owner.mutex.Lock()
	owner.value++
	committedValue := owner.value
	owner.mutex.Unlock()

	publishErr := counterAdvancedEvent.Publish(
		requestContext,
		owner.sourceScope,
		counterAdvanced{
			Value: committedValue,
		},
	)
	return committedValue, publishErr
}

type counterProviderPlugin struct {
	serviceObject *counterServiceObject
}

func (*counterProviderPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "example-counter-provider",
		Provides: []plugin.ServiceDefinition{
			counterServiceDefinition,
		},
	}
}

func (instance *counterProviderPlugin) Apply(
	_ context.Context,
	pluginContext *plugin.Context,
) error {
	instance.serviceObject = &counterServiceObject{
		sourceScope: pluginContext.Scope(),
	}
	return counterServiceDefinition.Provide(pluginContext, instance.serviceObject)
}

func (instance *counterProviderPlugin) Dispose(context.Context) error {
	instance.serviceObject = nil
	return nil
}

type counterEventObserver struct {
	valueOutput *int
}

func (observer *counterEventObserver) ObserveEvent(
	_ context.Context,
	fact counterAdvanced,
) error {
	*observer.valueOutput = fact.Value
	return nil
}

type counterListenerPlugin struct {
	valueOutput *int
}

func (*counterListenerPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "example-counter-listener",
	}
}

func (instance *counterListenerPlugin) Apply(
	_ context.Context,
	pluginContext *plugin.Context,
) error {
	return counterAdvancedEvent.Observe(
		pluginContext,
		&counterEventObserver{
			valueOutput: instance.valueOutput,
		},
	)
}

func (*counterListenerPlugin) Dispose(context.Context) error {
	return nil
}

type counterEndpoint struct {
	service counterService
}

func (endpoint *counterEndpoint) Advance(
	requestContext context.Context,
) (int, error) {
	return endpoint.service.Advance(requestContext)
}

type counterEndpointPlugin struct {
	endpointObject *counterEndpoint
}

func (*counterEndpointPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "example-counter-endpoint",
		Requires: []plugin.ServiceDefinition{
			counterServiceDefinition,
		},
	}
}

func (instance *counterEndpointPlugin) Apply(
	_ context.Context,
	pluginContext *plugin.Context,
) error {
	providedCounter, err := counterServiceDefinition.Require(pluginContext)
	if err != nil {
		return err
	}
	instance.endpointObject.service = providedCounter
	return nil
}

func (instance *counterEndpointPlugin) Dispose(context.Context) error {
	instance.endpointObject.service = nil
	return nil
}

func Example_servicePublishesEvent() {
	observedEventValue := 0
	endpointObject := &counterEndpoint{}
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	if _, err := runtimeEngine.Load(context.Background(), &counterProviderPlugin{}); err != nil {
		panic(err)
	}
	if _, err := runtimeEngine.Load(
		context.Background(),
		&counterListenerPlugin{
			valueOutput: &observedEventValue,
		},
	); err != nil {
		panic(err)
	}
	if _, err := runtimeEngine.Load(
		context.Background(),
		&counterEndpointPlugin{
			endpointObject: endpointObject,
		},
	); err != nil {
		panic(err)
	}

	committedValue, err := endpointObject.Advance(context.Background())
	if err != nil {
		panic(err)
	}
	fmt.Printf("service=%d event=%d\n", committedValue, observedEventValue)
	if err := runtimeEngine.Shutdown(context.Background()); err != nil {
		panic(err)
	}

	// Output:
	// service=1 event=1
}
