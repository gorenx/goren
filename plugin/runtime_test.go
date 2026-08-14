package plugin_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/gorenx/goren/plugin"
)

type textService interface {
	Text() string
}

type textValue string

func (providedValue textValue) Text() string {
	return string(providedValue)
}

var textServiceKey = plugin.DefineService[textService]("contract.text")

type fixturePlugin struct {
	metadata plugin.Manifest
	body     func(context.Context, *plugin.Scope) error
}

func (instance fixturePlugin) Manifest() plugin.Manifest {
	return instance.metadata
}

func (instance fixturePlugin) Apply(requestContext context.Context, pluginScope *plugin.Scope) error {
	return instance.body(requestContext, pluginScope)
}

func TestRuntimeWaitsForRequiredServiceAndRestartsConsumer(t *testing.T) {
	t.Parallel()
	requestContext := context.Background()
	engine := plugin.NewRuntime()
	transitions := []string{}
	consumerStarts := 0
	consumer := fixturePlugin{
		metadata: plugin.Manifest{Name: "consumer", Requires: []plugin.ServiceRef{textServiceKey.Ref()}},
		body: func(_ context.Context, pluginScope *plugin.Scope) error {
			dependency, found := plugin.Require(pluginScope, textServiceKey)
			if !found {
				return errors.New("required service missing during Apply")
			}
			consumerStarts++
			captured := dependency.Text()
			transitions = append(transitions, "consumer.start:"+captured)
			return pluginScope.Effect(requestContext, "consumer", func(context.Context) (plugin.Disposer, error) {
				return func(context.Context) error {
					transitions = append(transitions, "consumer.stop:"+captured)
					return nil
				}, nil
			})
		},
	}
	consumerHandle, err := engine.Load(requestContext, consumer)
	if err != nil {
		t.Fatal(err)
	}
	consumerStatus, err := engine.Status(consumerHandle)
	if err != nil {
		t.Fatal(err)
	}
	if consumerStatus.State != plugin.StateWaiting || consumerStarts != 0 {
		t.Fatalf("waiting consumer = (%s, %d starts)", consumerStatus.State, consumerStarts)
	}

	providerV1 := newTextProvider("provider-v1", "v1", &transitions)
	providerHandle, err := engine.Load(requestContext, providerV1)
	if err != nil {
		t.Fatal(err)
	}
	consumerStatus, err = engine.Status(consumerHandle)
	if err != nil {
		t.Fatal(err)
	}
	if consumerStatus.State != plugin.StateActive || consumerStarts != 1 {
		t.Fatalf("active consumer = (%s, %d starts)", consumerStatus.State, consumerStarts)
	}

	duplicate := newTextProvider("duplicate", "duplicate", &transitions)
	if _, err := engine.Load(requestContext, duplicate); err == nil {
		t.Fatal("duplicate provider load succeeded")
	}
	if err := engine.Unload(requestContext, providerHandle); err != nil {
		t.Fatal(err)
	}
	consumerStatus, err = engine.Status(consumerHandle)
	if err != nil {
		t.Fatal(err)
	}
	if consumerStatus.State != plugin.StateWaiting {
		t.Fatalf("consumer state after provider unload = %s", consumerStatus.State)
	}

	if _, err := engine.Load(requestContext, newTextProvider("provider-v2", "v2", &transitions)); err != nil {
		t.Fatal(err)
	}
	if consumerStarts != 2 {
		t.Fatalf("consumer starts = %d, want 2", consumerStarts)
	}
	want := []string{
		"provider.start:v1", "consumer.start:v1", "consumer.stop:v1", "provider.stop:v1",
		"provider.start:v2", "consumer.start:v2",
	}
	if !reflect.DeepEqual(transitions, want) {
		t.Fatalf("transitions = %#v, want %#v", transitions, want)
	}
}

func TestRuntimeRollsBackFailedApplyInLIFOOrder(t *testing.T) {
	t.Parallel()
	requestContext := context.Background()
	engine := plugin.NewRuntime()
	releases := []string{}
	broken := fixturePlugin{
		metadata: plugin.Manifest{Name: "broken", Provides: []plugin.ServiceRef{textServiceKey.Ref()}},
		body: func(_ context.Context, pluginScope *plugin.Scope) error {
			if _, err := plugin.Provide(pluginScope, textServiceKey, textService(textValue("broken"))); err != nil {
				return err
			}
			for _, label := range []string{"first", "second"} {
				captured := label
				if err := pluginScope.Effect(requestContext, captured, func(context.Context) (plugin.Disposer, error) {
					return func(context.Context) error {
						releases = append(releases, captured)
						return nil
					}, nil
				}); err != nil {
					return err
				}
			}
			return errors.New("startup failed")
		},
	}
	brokenHandle, err := engine.Load(requestContext, broken)
	if err == nil || err.Error() != "startup failed" {
		t.Fatalf("load error = %v", err)
	}
	if !reflect.DeepEqual(releases, []string{"second", "first"}) {
		t.Fatalf("release order = %#v", releases)
	}
	brokenStatus, statusErr := engine.Status(brokenHandle)
	if statusErr != nil {
		t.Fatal(statusErr)
	}
	if brokenStatus.State != plugin.StateFailed || len(brokenStatus.Effects) != 0 {
		t.Fatalf("failed status = %#v", brokenStatus)
	}
	if err := engine.Unload(requestContext, brokenHandle); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Load(requestContext, newTextProvider("healthy", "ok", &releases)); err != nil {
		t.Fatalf("service leaked from failed activation: %v", err)
	}
}

func TestRuntimeReplacementKeepsLastKnownGoodAndRestartsDependents(t *testing.T) {
	t.Parallel()
	requestContext := context.Background()
	engine := plugin.NewRuntime()
	transitions := []string{}
	providerHandle, err := engine.Load(requestContext, newTextProvider("provider", "v1", &transitions))
	if err != nil {
		t.Fatal(err)
	}
	consumer := fixturePlugin{
		metadata: plugin.Manifest{Name: "consumer", Requires: []plugin.ServiceRef{textServiceKey.Ref()}},
		body: func(_ context.Context, pluginScope *plugin.Scope) error {
			dependency, found := plugin.Require(pluginScope, textServiceKey)
			if !found {
				return errors.New("required service missing")
			}
			captured := dependency.Text()
			transitions = append(transitions, "consumer.start:"+captured)
			return pluginScope.Effect(requestContext, "consumer", func(context.Context) (plugin.Disposer, error) {
				return func(context.Context) error {
					transitions = append(transitions, "consumer.stop:"+captured)
					return nil
				}, nil
			})
		},
	}
	if _, err := engine.Load(requestContext, consumer); err != nil {
		t.Fatal(err)
	}

	failedCandidate := newTextProvider("provider", "bad", &transitions).(fixturePlugin)
	failedBody := failedCandidate.body
	failedCandidate.body = func(requestContext context.Context, pluginScope *plugin.Scope) error {
		if err := failedBody(requestContext, pluginScope); err != nil {
			return err
		}
		return errors.New("candidate failed")
	}
	if err := engine.Replace(requestContext, providerHandle, failedCandidate); err == nil {
		t.Fatal("failed replacement succeeded")
	}
	activeValue, found := currentText(engine, requestContext)
	if !found || activeValue != "v1" {
		t.Fatalf("last-known-good service = %q, %t", activeValue, found)
	}

	if err := engine.Replace(requestContext, providerHandle, newTextProvider("provider", "v2", &transitions)); err != nil {
		t.Fatal(err)
	}
	activeValue, found = currentText(engine, requestContext)
	if !found || activeValue != "v2" {
		t.Fatalf("replacement service = %q, %t", activeValue, found)
	}
	want := []string{
		"provider.start:v1", "consumer.start:v1",
		"provider.start:bad", "provider.stop:bad",
		"provider.start:v2", "consumer.stop:v1", "provider.stop:v1", "consumer.start:v2",
	}
	if !reflect.DeepEqual(transitions, want) {
		t.Fatalf("transitions = %#v, want %#v", transitions, want)
	}
}

func TestRuntimeShutdownIsDependentFirst(t *testing.T) {
	t.Parallel()
	requestContext := context.Background()
	engine := plugin.NewRuntime()
	transitions := []string{}
	if _, err := engine.Load(requestContext, newTextProvider("provider", "v1", &transitions)); err != nil {
		t.Fatal(err)
	}
	consumer := fixturePlugin{
		metadata: plugin.Manifest{Name: "consumer", Requires: []plugin.ServiceRef{textServiceKey.Ref()}},
		body: func(_ context.Context, pluginScope *plugin.Scope) error {
			transitions = append(transitions, "consumer.start")
			return pluginScope.Effect(requestContext, "consumer", func(context.Context) (plugin.Disposer, error) {
				return func(context.Context) error {
					transitions = append(transitions, "consumer.stop")
					return nil
				}, nil
			})
		},
	}
	if _, err := engine.Load(requestContext, consumer); err != nil {
		t.Fatal(err)
	}
	if err := engine.Shutdown(requestContext); err != nil {
		t.Fatal(err)
	}
	wantSuffix := []string{"consumer.stop", "provider.stop:v1"}
	if !reflect.DeepEqual(transitions[len(transitions)-2:], wantSuffix) {
		t.Fatalf("shutdown suffix = %#v", transitions)
	}
	if _, err := engine.Load(requestContext, consumer); err == nil {
		t.Fatal("load after shutdown succeeded")
	}
}

func TestProvidedServiceDisposerUpdatesLiveDependencyGraph(t *testing.T) {
	t.Parallel()
	requestContext := context.Background()
	engine := plugin.NewRuntime()
	transitions := []string{}
	var providerScope *plugin.Scope
	var withdraw plugin.Disposer
	provider := fixturePlugin{
		metadata: plugin.Manifest{Name: "dynamic-provider", Provides: []plugin.ServiceRef{textServiceKey.Ref()}},
		body: func(_ context.Context, pluginScope *plugin.Scope) error {
			providerScope = pluginScope
			var err error
			withdraw, err = plugin.Provide(pluginScope, textServiceKey, textService(textValue("v1")))
			return err
		},
	}
	if _, err := engine.Load(requestContext, provider); err != nil {
		t.Fatal(err)
	}
	consumerHandle, err := engine.Load(requestContext, fixturePlugin{
		metadata: plugin.Manifest{Name: "dynamic-consumer", Requires: []plugin.ServiceRef{textServiceKey.Ref()}},
		body: func(_ context.Context, pluginScope *plugin.Scope) error {
			dependency, found := plugin.Require(pluginScope, textServiceKey)
			if !found {
				return errors.New("dynamic service missing")
			}
			captured := dependency.Text()
			transitions = append(transitions, "start:"+captured)
			return pluginScope.Effect(requestContext, "consumer", func(context.Context) (plugin.Disposer, error) {
				return func(context.Context) error {
					transitions = append(transitions, "stop:"+captured)
					return nil
				}, nil
			})
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := withdraw(requestContext); err != nil {
		t.Fatal(err)
	}
	consumerStatus, err := engine.Status(consumerHandle)
	if err != nil {
		t.Fatal(err)
	}
	if consumerStatus.State != plugin.StateWaiting {
		t.Fatalf("consumer after withdrawal = %s", consumerStatus.State)
	}
	if _, err := plugin.Provide(providerScope, textServiceKey, textService(textValue("v2"))); err != nil {
		t.Fatal(err)
	}
	consumerStatus, err = engine.Status(consumerHandle)
	if err != nil {
		t.Fatal(err)
	}
	if consumerStatus.State != plugin.StateActive {
		t.Fatalf("consumer after re-provide = %s", consumerStatus.State)
	}
	want := []string{"start:v1", "stop:v1", "start:v2"}
	if !reflect.DeepEqual(transitions, want) {
		t.Fatalf("transitions = %#v, want %#v", transitions, want)
	}
}

func newTextProvider(providerName string, providedValue textValue, transitions *[]string) plugin.Plugin {
	return fixturePlugin{
		metadata: plugin.Manifest{Name: providerName, Provides: []plugin.ServiceRef{textServiceKey.Ref()}},
		body: func(requestContext context.Context, pluginScope *plugin.Scope) error {
			*transitions = append(*transitions, "provider.start:"+providedValue.Text())
			if _, err := plugin.Provide(pluginScope, textServiceKey, textService(providedValue)); err != nil {
				return err
			}
			return pluginScope.Effect(requestContext, "provider", func(context.Context) (plugin.Disposer, error) {
				return func(context.Context) error {
					*transitions = append(*transitions, "provider.stop:"+providedValue.Text())
					return nil
				}, nil
			})
		},
	}
}

func currentText(engine *plugin.Runtime, requestContext context.Context) (string, bool) {
	value := ""
	found := false
	probe := fixturePlugin{
		metadata: plugin.Manifest{Name: "probe", Optional: []plugin.ServiceRef{textServiceKey.Ref()}},
		body:     func(context.Context, *plugin.Scope) error { return nil },
	}
	probe.body = func(_ context.Context, pluginScope *plugin.Scope) error {
		dependency, available := plugin.Require(pluginScope, textServiceKey)
		if available {
			value = dependency.Text()
			found = true
		}
		return nil
	}
	probeHandle, err := engine.Load(requestContext, probe)
	if err != nil {
		return "", false
	}
	_ = engine.Unload(requestContext, probeHandle)
	return value, found
}
