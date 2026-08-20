package example_test

import (
	"context"
	"fmt"

	"github.com/gorenx/goren/plugin"
)

type greeting interface {
	plugin.Service
	Greet(string) string
}

type greetingPlugin struct {
	plugin.Base
	prefix string
}

func (*greetingPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "greeting",
		Provides: []plugin.ServiceType{
			plugin.ServiceOf[greeting](),
		},
	}
}

func (*greetingPlugin) Apply(context.Context) error {
	return nil
}

func (*greetingPlugin) Dispose(context.Context) error {
	return nil
}

func (serviceOwner *greetingPlugin) Greet(name string) string {
	return serviceOwner.prefix + ", " + name
}

type greetingEndpoint struct {
	plugin.Base
	greetings greeting
}

func (*greetingEndpoint) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "greeting-endpoint",
		Requires: []plugin.ServiceType{
			plugin.ServiceOf[greeting](),
		},
	}
}

func (endpoint *greetingEndpoint) Apply(context.Context) error {
	greetings, err := plugin.Require[greeting](endpoint)
	if err != nil {
		return err
	}
	endpoint.greetings = greetings
	return nil
}

func (endpoint *greetingEndpoint) Dispose(context.Context) error {
	endpoint.greetings = nil
	return nil
}

func Example_service() {
	serviceOwner := &greetingPlugin{
		prefix: "hello",
	}
	endpoint := &greetingEndpoint{}
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	_, err := runtimeEngine.Start(
		context.Background(),
		endpoint,
		serviceOwner,
	)
	if err != nil {
		panic(err)
	}
	defer runtimeEngine.Shutdown(context.Background())

	fmt.Println(endpoint.greetings.Greet("Goren"))
	// Output: hello, Goren
}
