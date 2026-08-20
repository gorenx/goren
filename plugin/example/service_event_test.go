package example_test

import (
	"context"
	"fmt"

	"github.com/gorenx/goren/plugin"
)

type greetingUsed struct {
	Name string
}

func (greetingUsed) EventName() string {
	return "greeting/used"
}

func (greetingUsed) EventDelivery() plugin.DeliveryPolicy {
	return plugin.DeliveryOrdered
}

type greetingListener struct {
	plugin.Base
	names []string
}

func (*greetingListener) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "greeting-listener",
		Events: []plugin.EventSubscription{
			plugin.EventOf[greetingUsed](),
		},
	}
}

func (*greetingListener) Apply(context.Context) error {
	return nil
}

func (*greetingListener) Dispose(context.Context) error {
	return nil
}

func (listener *greetingListener) ObserveEvent(
	_ context.Context,
	fact greetingUsed,
) error {
	listener.names = append(listener.names, fact.Name)
	return nil
}

func Example_servicePublishesEvent() {
	serviceOwner := &greetingPlugin{
		prefix: "hello",
	}
	listener := &greetingListener{}
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	if _, err := runtimeEngine.Start(
		context.Background(),
		serviceOwner,
		listener,
	); err != nil {
		panic(err)
	}
	defer runtimeEngine.Shutdown(context.Background())

	fmt.Println(serviceOwner.Greet("event"))
	if err := plugin.Publish(
		context.Background(),
		serviceOwner,
		greetingUsed{
			Name: "event",
		},
	); err != nil {
		panic(err)
	}
	fmt.Println(listener.names)
	// Output:
	// hello, event
	// [event]
}
