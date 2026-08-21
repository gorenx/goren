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

func (serviceOwner *greetingPlugin) GreetAndPublish(
	requestContext context.Context,
	name string,
) (string, error) {
	message := serviceOwner.Greet(name)
	if err := plugin.Publish(
		requestContext,
		serviceOwner,
		greetingUsed{
			Name: name,
		},
	); err != nil {
		return "", err
	}
	return message, nil
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
	requestContext context.Context,
	fact plugin.Event,
) error {
	if err := requestContext.Err(); err != nil {
		return err
	}
	usedGreeting, matches := fact.(greetingUsed)
	if !matches {
		return fmt.Errorf("greeting listener: unsupported Event %q", fact.EventName())
	}
	return listener.observeGreetingUsed(usedGreeting)
}

func (listener *greetingListener) observeGreetingUsed(fact greetingUsed) error {
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

	message, err := serviceOwner.GreetAndPublish(
		context.Background(),
		"event",
	)
	if err != nil {
		panic(err)
	}
	fmt.Println(message)
	fmt.Println(listener.names)
	// Output:
	// hello, event
	// [event]
}
