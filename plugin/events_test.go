package plugin_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gorenx/goren/plugin"
)

var (
	emitTopic      = plugin.DefineEvent[string, struct{}]("fixture.emit", plugin.ModeEmit)
	parallelTopic  = plugin.DefineEvent[string, struct{}]("fixture.parallel", plugin.ModeParallel)
	serialTopic    = plugin.DefineEvent[string, string]("fixture.serial", plugin.ModeSerial)
	bailTopic      = plugin.DefineEvent[string, string]("fixture.bail", plugin.ModeBail)
	waterfallTopic = plugin.DefineEvent[string, string]("fixture.waterfall", plugin.ModeWaterfall)
	scopedTopic    = plugin.DefineEvent[string, string]("fixture.scoped-waterfall", plugin.ModeWaterfall)
)

func TestEventEmitUsesRegistrationOrderAndScopeOwnership(t *testing.T) {
	t.Parallel()
	requestContext := context.Background()
	engine := plugin.NewRuntime()
	calls := []string{}
	firstHandle, err := engine.Load(requestContext, fixturePlugin{
		metadata: plugin.Manifest{Name: "emit-first"},
		body: func(_ context.Context, pluginScope *plugin.Scope) error {
			_, registrationErr := plugin.OnNotify(pluginScope, emitTopic, func(_ context.Context, payload string) error {
				calls = append(calls, "first:"+payload)
				return nil
			})
			return registrationErr
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("listener failed")
	if _, err := engine.Load(requestContext, fixturePlugin{
		metadata: plugin.Manifest{Name: "emit-second"},
		body: func(_ context.Context, pluginScope *plugin.Scope) error {
			_, registrationErr := plugin.OnNotify(pluginScope, emitTopic, func(_ context.Context, payload string) error {
				calls = append(calls, "second:"+payload)
				return sentinel
			})
			return registrationErr
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := plugin.Emit(requestContext, engine, emitTopic, "one"); !errors.Is(err, sentinel) {
		t.Fatalf("emit error = %v", err)
	}
	if err := engine.Unload(requestContext, firstHandle); err != nil {
		t.Fatal(err)
	}
	if err := plugin.Emit(requestContext, engine, emitTopic, "two"); !errors.Is(err, sentinel) {
		t.Fatalf("emit after unload error = %v", err)
	}
	want := []string{"first:one", "second:one", "second:two"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestEmitSnapshotFreezesListenerSet(t *testing.T) {
	t.Parallel()
	requestContext := context.Background()
	engine := plugin.NewRuntime()
	calls := []string{}
	var sourceScope *plugin.Scope
	firstHandle, err := engine.Load(requestContext, fixturePlugin{
		metadata: plugin.Manifest{Name: "snapshot-first"},
		body: func(_ context.Context, pluginScope *plugin.Scope) error {
			sourceScope = pluginScope
			_, registrationErr := plugin.OnNotify(pluginScope, emitTopic, func(context.Context, string) error {
				calls = append(calls, "first")
				return nil
			})
			return registrationErr
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	captured, err := plugin.CaptureEmitFrom(sourceScope, emitTopic)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Load(requestContext, fixturePlugin{
		metadata: plugin.Manifest{Name: "snapshot-second"},
		body: func(_ context.Context, pluginScope *plugin.Scope) error {
			_, registrationErr := plugin.OnNotify(pluginScope, emitTopic, func(context.Context, string) error {
				calls = append(calls, "second")
				return nil
			})
			return registrationErr
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := engine.Unload(requestContext, firstHandle); err != nil {
		t.Fatal(err)
	}
	if err := captured.Dispatch(requestContext, "captured"); err != nil {
		t.Fatal(err)
	}
	if err := plugin.Emit(requestContext, engine, emitTopic, "live"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"first", "second"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestEventParallelStartsAllListenersBeforeWaiting(t *testing.T) {
	t.Parallel()
	requestContext := context.Background()
	engine := plugin.NewRuntime()
	started := make(chan string, 2)
	release := make(chan struct{})
	sentinel := errors.New("parallel failure")
	for _, fixture := range []struct {
		label string
		err   error
	}{{label: "first"}, {label: "second", err: sentinel}} {
		captured := fixture
		if _, err := engine.Load(requestContext, fixturePlugin{
			metadata: plugin.Manifest{Name: "parallel-" + captured.label},
			body: func(_ context.Context, pluginScope *plugin.Scope) error {
				_, registrationErr := plugin.OnNotify(pluginScope, parallelTopic, func(context.Context, string) error {
					started <- captured.label
					<-release
					return captured.err
				})
				return registrationErr
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	settled := make(chan error, 1)
	go func() {
		settled <- plugin.Parallel(requestContext, engine, parallelTopic, "payload")
	}()
	seen := map[string]bool{}
	for len(seen) != 2 {
		select {
		case label := <-started:
			seen[label] = true
		case <-time.After(time.Second):
			t.Fatal("parallel listener did not start")
		}
	}
	close(release)
	if err := <-settled; !errors.Is(err, sentinel) {
		t.Fatalf("parallel error = %v", err)
	}
}

func TestEventSerialAndBailStopOnExplicitDecision(t *testing.T) {
	t.Parallel()
	requestContext := context.Background()
	for _, fixture := range []struct {
		label    string
		topic    plugin.EventKey[string, string]
		dispatch func(context.Context, *plugin.Runtime, plugin.EventKey[string, string], string) (plugin.Decision[string], error)
	}{
		{label: "serial", topic: serialTopic, dispatch: plugin.Serial[string, string]},
		{label: "bail", topic: bailTopic, dispatch: plugin.Bail[string, string]},
	} {
		fixture := fixture
		t.Run(fixture.label, func(t *testing.T) {
			t.Parallel()
			engine := plugin.NewRuntime()
			calls := []string{}
			for index := 0; index < 3; index++ {
				captured := index
				if _, err := engine.Load(requestContext, fixturePlugin{
					metadata: plugin.Manifest{Name: fixture.label + "-listener-" + string(rune('0'+index))},
					body: func(_ context.Context, pluginScope *plugin.Scope) error {
						_, registrationErr := plugin.OnDecision(pluginScope, fixture.topic,
							func(context.Context, string) (plugin.Decision[string], error) {
								calls = append(calls, string(rune('0'+captured)))
								return plugin.Decision[string]{Value: "selected", Bail: captured == 1}, nil
							})
						return registrationErr
					},
				}); err != nil {
					t.Fatal(err)
				}
			}
			outcome, err := fixture.dispatch(requestContext, engine, fixture.topic, "payload")
			if err != nil {
				t.Fatal(err)
			}
			if !outcome.Bail || outcome.Value != "selected" || !reflect.DeepEqual(calls, []string{"0", "1"}) {
				t.Fatalf("outcome = %#v, calls = %#v", outcome, calls)
			}
		})
	}
}

func TestEventWaterfallPreservesOuterToInnerControl(t *testing.T) {
	t.Parallel()
	requestContext := context.Background()
	engine := plugin.NewRuntime()
	calls := []string{}
	for _, fixture := range []struct {
		label string
		mark  string
	}{{label: "outer", mark: "1"}, {label: "inner", mark: "2"}} {
		captured := fixture
		if _, err := engine.Load(requestContext, fixturePlugin{
			metadata: plugin.Manifest{Name: "waterfall-" + captured.label},
			body: func(_ context.Context, pluginScope *plugin.Scope) error {
				_, registrationErr := plugin.OnWaterfall(pluginScope, waterfallTopic,
					func(chainContext context.Context, payload string, downstream plugin.Next[string, string]) (string, error) {
						calls = append(calls, captured.label+".before:"+payload)
						innerValue, err := downstream(chainContext, payload+captured.mark)
						calls = append(calls, captured.label+".after:"+innerValue)
						return captured.label + "(" + innerValue + ")", err
					})
				return registrationErr
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	value, err := plugin.Waterfall(requestContext, engine, waterfallTopic, "x",
		func(_ context.Context, payload string) (string, error) {
			calls = append(calls, "terminal:"+payload)
			return strings.ToUpper(payload), nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if value != "outer(inner(X12))" {
		t.Fatalf("waterfall value = %q", value)
	}
	want := []string{
		"outer.before:x", "inner.before:x1", "terminal:x12",
		"inner.after:X12", "outer.after:inner(X12)",
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestScopedWaterfallAdmitsGlobalAndAncestorListeners(t *testing.T) {
	t.Parallel()
	requestContext := context.Background()
	engine := plugin.NewRuntime()
	var childScope *plugin.Scope
	var nestedScope *plugin.Scope
	var siblingScope *plugin.Scope
	if _, err := engine.Load(requestContext, fixturePlugin{
		metadata: plugin.Manifest{Name: "scoped-waterfall-owner"},
		body: func(_ context.Context, pluginScope *plugin.Scope) error {
			var childErr error
			childScope, _, childErr = pluginScope.Child("child")
			if childErr != nil {
				return childErr
			}
			nestedScope, _, childErr = childScope.Child("nested")
			if childErr != nil {
				return childErr
			}
			siblingScope, _, childErr = pluginScope.Child("sibling")
			if childErr != nil {
				return childErr
			}
			for _, entry := range []struct {
				owner *plugin.Scope
				mark  string
			}{
				{owner: pluginScope, mark: "root"},
				{owner: childScope, mark: "child"},
				{owner: nestedScope, mark: "nested"},
				{owner: siblingScope, mark: "sibling"},
			} {
				captured := entry
				if _, registrationErr := plugin.OnWaterfall(captured.owner, scopedTopic,
					func(chainContext context.Context, payload string, downstream plugin.Next[string, string]) (string, error) {
						return downstream(chainContext, payload+"/"+captured.mark)
					}); registrationErr != nil {
					return registrationErr
				}
			}
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	terminal := func(_ context.Context, payload string) (string, error) { return payload, nil }
	globalValue, err := plugin.WaterfallScopedFrom(requestContext, childScope, plugin.ScopeKey{}, scopedTopic, "x", terminal)
	if err != nil {
		t.Fatal(err)
	}
	if globalValue != "x/root" {
		t.Fatalf("global scoped waterfall = %q", globalValue)
	}
	nestedValue, err := plugin.WaterfallScopedFrom(requestContext, childScope, nestedScope.Target(), scopedTopic, "x", terminal)
	if err != nil {
		t.Fatal(err)
	}
	if nestedValue != "x/root/child/nested" {
		t.Fatalf("nested scoped waterfall = %q", nestedValue)
	}
	siblingValue, err := plugin.WaterfallScopedFrom(requestContext, childScope, siblingScope.Target(), scopedTopic, "x", terminal)
	if err != nil {
		t.Fatal(err)
	}
	if siblingValue != "x/root/sibling" {
		t.Fatalf("sibling scoped waterfall = %q", siblingValue)
	}
	unfilteredValue, err := plugin.Waterfall(requestContext, engine, scopedTopic, "x", terminal)
	if err != nil {
		t.Fatal(err)
	}
	if unfilteredValue != "x/root/child/nested/sibling" {
		t.Fatalf("unfiltered waterfall = %q", unfilteredValue)
	}

	lineage := plugin.ScopeLineage(nestedScope.Target())
	if len(lineage) != 2 || lineage[0] != childScope.Target() || lineage[1] != nestedScope.Target() {
		t.Fatalf("nested lineage = %#v", lineage)
	}
}

func TestEventOwnerKeyCannotBeRecreatedByName(t *testing.T) {
	t.Parallel()
	requestContext := context.Background()
	engine := plugin.NewRuntime()
	firstTopic := plugin.DefineEvent[string, struct{}]("fixture.owner", plugin.ModeEmit)
	secondTopic := plugin.DefineEvent[string, struct{}]("fixture.owner", plugin.ModeEmit)
	if _, err := engine.Load(requestContext, fixturePlugin{
		metadata: plugin.Manifest{Name: "owner"},
		body: func(_ context.Context, pluginScope *plugin.Scope) error {
			_, registrationErr := plugin.OnNotify(pluginScope, firstTopic, func(context.Context, string) error { return nil })
			return registrationErr
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Load(requestContext, fixturePlugin{
		metadata: plugin.Manifest{Name: "counterfeit"},
		body: func(_ context.Context, pluginScope *plugin.Scope) error {
			_, registrationErr := plugin.OnNotify(pluginScope, secondTopic, func(context.Context, string) error { return nil })
			return registrationErr
		},
	}); err == nil {
		t.Fatal("recreated event key was accepted")
	}
}
