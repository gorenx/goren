package example_test

import (
	"context"
	"fmt"

	"github.com/gorenx/goren/plugin"
)

type themeService interface {
	plugin.Service
	Theme() string
}

var themeServiceDefinition = plugin.DefineService[themeService]("example/theme")

type themeServiceObject struct {
	plugin.ServiceBase
	value string
}

func (owner *themeServiceObject) Theme() string {
	return owner.value
}

type themeProviderPlugin struct {
	pluginName     string
	value          string
	mountedPlugins []plugin.Plugin
}

func (instance *themeProviderPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: instance.pluginName,
		Provides: []plugin.ServiceDefinition{
			themeServiceDefinition,
		},
	}
}

func (instance *themeProviderPlugin) Apply(
	applyContext context.Context,
	pluginContext *plugin.Context,
) error {
	if err := themeServiceDefinition.Provide(
		pluginContext,
		&themeServiceObject{
			value: instance.value,
		},
	); err != nil {
		return err
	}
	for _, mountedPlugin := range instance.mountedPlugins {
		if _, err := pluginContext.Mount(applyContext, mountedPlugin); err != nil {
			return err
		}
	}
	return nil
}

func (*themeProviderPlugin) Dispose(context.Context) error {
	return nil
}

type themeConsumerPlugin struct {
	pluginName  string
	valueOutput *string
}

func (instance *themeConsumerPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: instance.pluginName,
		Requires: []plugin.ServiceDefinition{
			themeServiceDefinition,
		},
	}
}

func (instance *themeConsumerPlugin) Apply(
	_ context.Context,
	pluginContext *plugin.Context,
) error {
	providedTheme, err := themeServiceDefinition.Require(pluginContext)
	if err != nil {
		return err
	}
	*instance.valueOutput = providedTheme.Theme()
	return nil
}

func (*themeConsumerPlugin) Dispose(context.Context) error {
	return nil
}

func Example_scopeInheritance() {
	inheritedValue := ""
	overriddenValue := ""
	rootProvider := &themeProviderPlugin{
		pluginName: "example-root-theme-provider",
		value:      "root",
		mountedPlugins: []plugin.Plugin{
			&themeConsumerPlugin{
				pluginName:  "example-inherited-theme-consumer",
				valueOutput: &inheritedValue,
			},
			&themeProviderPlugin{
				pluginName: "example-child-theme-provider",
				value:      "child",
				mountedPlugins: []plugin.Plugin{
					&themeConsumerPlugin{
						pluginName:  "example-overridden-theme-consumer",
						valueOutput: &overriddenValue,
					},
				},
			},
		},
	}
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	if _, err := runtimeEngine.Load(context.Background(), rootProvider); err != nil {
		panic(err)
	}

	fmt.Println("inherited=" + inheritedValue)
	fmt.Println("nearest=" + overriddenValue)
	if err := runtimeEngine.Shutdown(context.Background()); err != nil {
		panic(err)
	}

	// Output:
	// inherited=root
	// nearest=child
}
