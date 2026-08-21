package agent_test

import (
	"context"
	"reflect"
	"testing"

	agentcore "github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
)

type statusObserver struct {
	plugin.Base
	name  string
	label string
	heard *[]string
}

func (observer *statusObserver) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: observer.name,
		Events: []plugin.EventSubscription{
			plugin.EventOf[agentcore.StatusChanged](),
		},
	}
}

func (*statusObserver) Apply(context.Context) error   { return nil }
func (*statusObserver) Dispose(context.Context) error { return nil }
func (observer *statusObserver) ObserveEvent(
	_ context.Context,
	fact plugin.Event,
) error {
	notice := fact.(agentcore.StatusChanged)
	*observer.heard = append(
		*observer.heard,
		observer.label+":"+string(notice.Subject.ID()),
	)
	return nil
}

func TestAgentEventsUseAgentFiberScope(t *testing.T) {
	t.Parallel()
	heard := make([]string, 0)
	global := &statusObserver{
		name:  "global-status-observer",
		label: "global",
		heard: &heard,
	}
	runtimeEngine, _, registryHandle := startRegistry(t, global)
	subject, subjectHandle := mountFakeAgent(
		t,
		runtimeEngine,
		registryHandle,
		"selected",
	)
	exact := &statusObserver{
		name:  "exact-status-observer",
		label: "exact",
		heard: &heard,
	}
	if _, err := runtimeEngine.MountChild(
		context.Background(),
		subjectHandle,
		exact,
	); err != nil {
		t.Fatal(err)
	}
	siblingRoot := &statusObserver{
		name:  "sibling-status-observer",
		label: "sibling",
		heard: &heard,
	}
	if _, err := runtimeEngine.MountScopedChild(
		context.Background(),
		registryHandle,
		siblingRoot,
	); err != nil {
		t.Fatal(err)
	}
	if err := plugin.Publish(
		context.Background(),
		subject,
		agentcore.StatusChanged{
			Subject: subject,
			Status:  agentcore.StatusRunning,
		},
	); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"exact:selected",
		"global:selected",
	}
	if !reflect.DeepEqual(heard, want) {
		t.Fatalf("heard = %#v, want %#v", heard, want)
	}
}

type requestMiddleware struct {
	label string
	model string
	order *[]string
}

func (middleware *requestMiddleware) Intercept(
	requestContext context.Context,
	notice agentcore.RequestNotice,
	downstream plugin.WaterfallAction[
		agentcore.RequestNotice,
		agentcore.RequestResolution,
	],
) (agentcore.RequestResolution, error) {
	*middleware.order = append(*middleware.order, middleware.label+"-before")
	resolved, err := downstream.Execute(requestContext, notice)
	*middleware.order = append(*middleware.order, middleware.label+"-after")
	if middleware.model != "" {
		resolved.Config.Model = middleware.model
	}
	return resolved, err
}

type requestPolicyPlugin struct {
	plugin.Base
	name       string
	middleware requestMiddleware
	order      *[]string
	stopping   bool
}

func (owner *requestPolicyPlugin) Manifest() plugin.Manifest {
	events := []plugin.EventSubscription(nil)
	if owner.stopping {
		events = []plugin.EventSubscription{
			plugin.EventOf[agentcore.TurnStopping](),
		}
	}
	return plugin.Manifest{
		Name:   owner.name,
		Events: events,
		Waterfalls: []plugin.WaterfallMiddlewareBinding{
			plugin.WaterfallOf[
				agentcore.RequestNotice,
				agentcore.RequestResolution,
			](&owner.middleware),
		},
	}
}

func (*requestPolicyPlugin) Apply(context.Context) error   { return nil }
func (*requestPolicyPlugin) Dispose(context.Context) error { return nil }
func (owner *requestPolicyPlugin) ObserveEvent(
	context.Context,
	plugin.Event,
) error {
	*owner.order = append(*owner.order, "stopping")
	return nil
}

func TestAgentWaterfallsAndEventsAreScoped(t *testing.T) {
	t.Parallel()
	order := make([]string, 0)
	global := &requestPolicyPlugin{
		name: "global-request-policy",
		middleware: requestMiddleware{
			label: "global",
			order: &order,
		},
		order: &order,
	}
	runtimeEngine, _, registryHandle := startRegistry(t, global)
	subject, subjectHandle := mountFakeAgent(
		t,
		runtimeEngine,
		registryHandle,
		"waterfall",
	)
	exact := &requestPolicyPlugin{
		name: "exact-request-policy",
		middleware: requestMiddleware{
			label: "exact",
			model: "scoped",
			order: &order,
		},
		order:    &order,
		stopping: true,
	}
	if _, err := runtimeEngine.MountChild(
		context.Background(),
		subjectHandle,
		exact,
	); err != nil {
		t.Fatal(err)
	}
	resolved, err := agentcore.ResolveRequest(
		context.Background(),
		agentcore.RequestNotice{
			Subject: subject,
			Turn:    1,
			Step:    1,
		},
		agentcore.RequestActionFunc(func(
			context.Context,
			agentcore.RequestNotice,
		) (agentcore.RequestResolution, error) {
			order = append(order, "terminal")
			return agentcore.RequestResolution{
				Config: llm.CallConfig{
					Provider: "fake",
					Model:    "base",
				},
			}, nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Model != "scoped" {
		t.Fatalf("resolved model = %q", resolved.Model)
	}
	if err := plugin.Publish(
		context.Background(),
		subject,
		agentcore.TurnStopping{
			Subject: subject,
			Turn:    1,
		},
	); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"global-before",
		"exact-before",
		"terminal",
		"exact-after",
		"global-after",
		"stopping",
	}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("order = %#v, want %#v", order, want)
	}
}
