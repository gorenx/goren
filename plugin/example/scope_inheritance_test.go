package example_test

import (
	"context"
	"fmt"

	"github.com/gorenx/goren/plugin"
)

func Example_scopeInheritance() {
	rootService := &greetingPlugin{
		prefix: "root",
	}
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	rootHandles, err := runtimeEngine.Start(context.Background(), rootService)
	if err != nil {
		panic(err)
	}
	defer runtimeEngine.Shutdown(context.Background())

	inheritedEndpoint := &greetingEndpoint{}
	if _, err := runtimeEngine.MountChild(
		context.Background(),
		rootHandles[0],
		inheritedEndpoint,
	); err != nil {
		panic(err)
	}

	childService := &greetingPlugin{
		prefix: "child",
	}
	childHandle, err := runtimeEngine.MountChild(
		context.Background(),
		rootHandles[0],
		childService,
	)
	if err != nil {
		panic(err)
	}
	overriddenEndpoint := &greetingEndpoint{}
	if _, err := runtimeEngine.MountChild(
		context.Background(),
		childHandle,
		overriddenEndpoint,
	); err != nil {
		panic(err)
	}

	fmt.Println(inheritedEndpoint.greetings.Greet("scope"))
	fmt.Println(overriddenEndpoint.greetings.Greet("scope"))
	// Output:
	// root, scope
	// child, scope
}
