package plugin_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/gorenx/goren/plugin"
)

type benchmarkEvent struct{}

func (benchmarkEvent) EventName() string {
	return "benchmark/event"
}

func (benchmarkEvent) EventDelivery() plugin.DeliveryPolicy {
	return plugin.DeliveryOrdered
}

type benchmarkPlugin struct {
	plugin.Base
	name string
}

func (candidate *benchmarkPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: candidate.name,
	}
}

func (*benchmarkPlugin) Apply(context.Context) error {
	return nil
}

func (*benchmarkPlugin) Dispose(context.Context) error {
	return nil
}

type benchmarkObserver struct {
	benchmarkPlugin
}

func (observer *benchmarkObserver) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: observer.name,
		Events: []plugin.EventSubscription{
			plugin.EventOf[benchmarkEvent](),
		},
	}
}

func (*benchmarkObserver) ObserveEvent(context.Context, plugin.Event) error {
	return nil
}

type benchmarkInput struct {
	plugin.WaterfallInputBase
}

type benchmarkOutput struct {
	plugin.WaterfallOutputBase
}

type benchmarkMiddleware struct {
	benchmarkPlugin
}

func (middleware *benchmarkMiddleware) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: middleware.name,
		Waterfalls: []plugin.WaterfallMiddlewareBinding{
			plugin.WaterfallOf[benchmarkInput, benchmarkOutput](middleware),
		},
	}
}

func (*benchmarkMiddleware) Intercept(
	requestContext context.Context,
	input benchmarkInput,
	downstream plugin.WaterfallAction[benchmarkInput, benchmarkOutput],
) (benchmarkOutput, error) {
	return downstream.Execute(requestContext, input)
}

type benchmarkAction struct{}

func (benchmarkAction) Execute(
	context.Context,
	benchmarkInput,
) (benchmarkOutput, error) {
	return benchmarkOutput{}, nil
}

type benchmarkService interface {
	plugin.Service
	Ready() bool
}

type benchmarkProvider struct {
	benchmarkPlugin
}

func (provider *benchmarkProvider) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: provider.name,
		Provides: []plugin.ProvidedService{
			plugin.NewProvidedService[benchmarkService](provider),
		},
	}
}

func (*benchmarkProvider) Ready() bool {
	return true
}

type benchmarkConsumer struct {
	benchmarkPlugin
	selected benchmarkService
}

func (consumer *benchmarkConsumer) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: consumer.name,
		Requires: []plugin.ServiceType{
			plugin.ServiceOf[benchmarkService](),
		},
	}
}

func (consumer *benchmarkConsumer) Apply(context.Context) error {
	selected, err := plugin.Require[benchmarkService](consumer)
	if err != nil {
		return err
	}
	consumer.selected = selected
	return nil
}

func (consumer *benchmarkConsumer) Dispose(context.Context) error {
	consumer.selected = nil
	return nil
}

func BenchmarkRuntimeLifecycle(b *testing.B) {
	for _, pluginCount := range []int{1, 16, 64} {
		b.Run(fmt.Sprintf("plugins=%d", pluginCount), func(b *testing.B) {
			instances := make([]plugin.Plugin, 0, pluginCount)
			for pluginIndex := 0; pluginIndex < pluginCount; pluginIndex++ {
				instances = append(instances, &benchmarkPlugin{
					name: fmt.Sprintf("plugin-%d", pluginIndex),
				})
			}
			b.ReportAllocs()
			for b.Loop() {
				runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
				if _, err := runtimeEngine.Start(context.Background(), instances...); err != nil {
					b.Fatal(err)
				}
				if err := runtimeEngine.Shutdown(context.Background()); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkRuntimeDependencyOrdering(b *testing.B) {
	for _, pluginCount := range []int{16, 64, 256} {
		for _, providerFirst := range []bool{true, false} {
			label := "provider=last"
			if providerFirst {
				label = "provider=first"
			}
			b.Run(fmt.Sprintf("plugins=%d/%s", pluginCount, label), func(b *testing.B) {
				provider := &benchmarkProvider{
					benchmarkPlugin: benchmarkPlugin{
						name: "provider",
					},
				}
				consumer := &benchmarkConsumer{
					benchmarkPlugin: benchmarkPlugin{
						name: "consumer",
					},
				}
				instances := make([]plugin.Plugin, 0, pluginCount)
				if providerFirst {
					instances = append(instances, provider, consumer)
				} else {
					instances = append(instances, consumer)
				}
				for pluginIndex := len(instances); pluginIndex < pluginCount; pluginIndex++ {
					instances = append(instances, &benchmarkPlugin{
						name: fmt.Sprintf("plugin-%d", pluginIndex),
					})
				}
				if !providerFirst {
					instances[len(instances)-1] = provider
				}
				b.ReportAllocs()
				for b.Loop() {
					runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
					if _, err := runtimeEngine.Start(
						context.Background(),
						instances...,
					); err != nil {
						b.Fatal(err)
					}
					if err := runtimeEngine.Shutdown(context.Background()); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

func BenchmarkPublishOrdered(b *testing.B) {
	for _, observerCount := range []int{0, 1, 8, 32} {
		b.Run(fmt.Sprintf("observers=%d", observerCount), func(b *testing.B) {
			instances := make([]plugin.Plugin, 0, observerCount+1)
			for observerIndex := 0; observerIndex < observerCount; observerIndex++ {
				instances = append(instances, &benchmarkObserver{
					benchmarkPlugin: benchmarkPlugin{
						name: fmt.Sprintf("observer-%d", observerIndex),
					},
				})
			}
			publisher := &benchmarkPlugin{
				name: "publisher",
			}
			instances = append(instances, publisher)
			runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
			if _, err := runtimeEngine.Start(context.Background(), instances...); err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() {
				if err := runtimeEngine.Shutdown(context.Background()); err != nil {
					b.Error(err)
				}
			})

			requestContext := context.Background()
			fact := benchmarkEvent{}
			b.ReportAllocs()
			for b.Loop() {
				if err := plugin.Publish(requestContext, publisher, fact); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkPublishOrderedScoped(b *testing.B) {
	for _, observerCount := range []int{1, 8, 32} {
		b.Run(fmt.Sprintf("observers=%d", observerCount), func(b *testing.B) {
			rootObserver := &benchmarkObserver{
				benchmarkPlugin: benchmarkPlugin{
					name: "observer-0",
				},
			}
			runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
			handles, err := runtimeEngine.Start(context.Background(), rootObserver)
			if err != nil {
				b.Fatal(err)
			}
			parentHandle := handles[0]
			for observerIndex := 1; observerIndex < observerCount; observerIndex++ {
				observer := &benchmarkObserver{
					benchmarkPlugin: benchmarkPlugin{
						name: fmt.Sprintf("observer-%d", observerIndex),
					},
				}
				parentHandle, err = runtimeEngine.MountScopedChild(
					context.Background(),
					parentHandle,
					observer,
				)
				if err != nil {
					b.Fatal(err)
				}
			}
			publisher := &benchmarkPlugin{
				name: "publisher",
			}
			if _, err = runtimeEngine.MountScopedChild(
				context.Background(),
				parentHandle,
				publisher,
			); err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() {
				if shutdownErr := runtimeEngine.Shutdown(context.Background()); shutdownErr != nil {
					b.Error(shutdownErr)
				}
			})

			requestContext := context.Background()
			fact := benchmarkEvent{}
			b.ReportAllocs()
			for b.Loop() {
				if publishErr := plugin.Publish(requestContext, publisher, fact); publishErr != nil {
					b.Fatal(publishErr)
				}
			}
		})
	}
}

func BenchmarkRunWaterfall(b *testing.B) {
	for _, middlewareCount := range []int{0, 1, 8, 32} {
		b.Run(fmt.Sprintf("middleware=%d", middlewareCount), func(b *testing.B) {
			instances := make([]plugin.Plugin, 0, middlewareCount+1)
			for middlewareIndex := 0; middlewareIndex < middlewareCount; middlewareIndex++ {
				instances = append(instances, &benchmarkMiddleware{
					benchmarkPlugin: benchmarkPlugin{
						name: fmt.Sprintf("middleware-%d", middlewareIndex),
					},
				})
			}
			source := &benchmarkPlugin{
				name: "source",
			}
			instances = append(instances, source)
			runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
			if _, err := runtimeEngine.Start(context.Background(), instances...); err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() {
				if err := runtimeEngine.Shutdown(context.Background()); err != nil {
					b.Error(err)
				}
			})

			requestContext := context.Background()
			input := benchmarkInput{}
			terminal := benchmarkAction{}
			b.ReportAllocs()
			for b.Loop() {
				if _, err := plugin.Run(
					requestContext,
					source,
					input,
					terminal,
				); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkRunWaterfallScoped(b *testing.B) {
	for _, middlewareCount := range []int{1, 8, 32} {
		b.Run(fmt.Sprintf("middleware=%d", middlewareCount), func(b *testing.B) {
			rootMiddleware := &benchmarkMiddleware{
				benchmarkPlugin: benchmarkPlugin{
					name: "middleware-0",
				},
			}
			runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
			handles, err := runtimeEngine.Start(context.Background(), rootMiddleware)
			if err != nil {
				b.Fatal(err)
			}
			parentHandle := handles[0]
			for middlewareIndex := 1; middlewareIndex < middlewareCount; middlewareIndex++ {
				middleware := &benchmarkMiddleware{
					benchmarkPlugin: benchmarkPlugin{
						name: fmt.Sprintf("middleware-%d", middlewareIndex),
					},
				}
				parentHandle, err = runtimeEngine.MountScopedChild(
					context.Background(),
					parentHandle,
					middleware,
				)
				if err != nil {
					b.Fatal(err)
				}
			}
			source := &benchmarkPlugin{
				name: "source",
			}
			if _, err = runtimeEngine.MountScopedChild(
				context.Background(),
				parentHandle,
				source,
			); err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() {
				if shutdownErr := runtimeEngine.Shutdown(context.Background()); shutdownErr != nil {
					b.Error(shutdownErr)
				}
			})

			requestContext := context.Background()
			input := benchmarkInput{}
			terminal := benchmarkAction{}
			b.ReportAllocs()
			for b.Loop() {
				if _, runErr := plugin.Run(
					requestContext,
					source,
					input,
					terminal,
				); runErr != nil {
					b.Fatal(runErr)
				}
			}
		})
	}
}
