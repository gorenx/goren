package agent_test

import (
	"context"
	"reflect"
	"testing"

	agentcore "github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
)

func TestAgentEventsFuseSubjectAndScope(t *testing.T) {
	t.Parallel()
	heard := []string{}
	_, _, providerScope := mountRegistry(t, func(_ agentcore.Registry, pluginScope *plugin.Scope) error {
		if _, err := agentcore.OnStatus(pluginScope, func(_ context.Context, subject agentcore.Agent, status agentcore.Status) error {
			heard = append(heard, "global:"+string(subject.ID())+":"+string(status))
			return nil
		}); err != nil {
			return err
		}
		return nil
	})
	subject, _ := newFakeAgent(t, providerScope, "selected")
	sibling, _, err := providerScope.Child("sibling-listener")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agentcore.OnStatus(subject.ScopeValue(), func(_ context.Context, current agentcore.Agent, _ agentcore.Status) error {
		heard = append(heard, "exact:"+string(current.ID()))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := agentcore.OnStatus(sibling, func(_ context.Context, current agentcore.Agent, _ agentcore.Status) error {
		heard = append(heard, "sibling:"+string(current.ID()))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := agentcore.EmitStatus(context.Background(), providerScope, subject, agentcore.StatusRunning); err != nil {
		t.Fatal(err)
	}
	want := []string{"global:selected:running", "exact:selected"}
	if !reflect.DeepEqual(heard, want) {
		t.Fatalf("heard = %#v, want %#v", heard, want)
	}
}

func TestAgentWaterfallsAndSerialAreScoped(t *testing.T) {
	t.Parallel()
	_, _, providerScope := mountRegistry(t, nil)
	subject, _ := newFakeAgent(t, providerScope, "waterfall")
	order := []string{}
	if _, err := agentcore.OnRequest(providerScope, func(requestContext context.Context, _ agentcore.RequestNotice, downstream agentcore.RequestNext) (llm.CallConfig, error) {
		order = append(order, "global-before")
		resolved, err := downstream(requestContext)
		order = append(order, "global-after")
		return resolved, err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := agentcore.OnRequest(subject.ScopeValue(), func(requestContext context.Context, _ agentcore.RequestNotice, downstream agentcore.RequestNext) (llm.CallConfig, error) {
		order = append(order, "exact-before")
		resolved, err := downstream(requestContext)
		order = append(order, "exact-after")
		resolved.Model = "scoped"
		return resolved, err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := agentcore.OnTurnStopping(subject.ScopeValue(), func(context.Context, agentcore.Agent, int64) error {
		order = append(order, "stopping")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	resolved, err := agentcore.ResolveRequest(context.Background(), providerScope, agentcore.RequestNotice{
		Subject: subject, Turn: 1, Step: 1,
	}, func(context.Context) (llm.CallConfig, error) {
		order = append(order, "terminal")
		return llm.CallConfig{Provider: "fake", Model: "base"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Model != "scoped" {
		t.Fatalf("resolved model = %q", resolved.Model)
	}
	if err := agentcore.DispatchTurnStopping(context.Background(), providerScope, subject, 1); err != nil {
		t.Fatal(err)
	}
	want := []string{"global-before", "exact-before", "terminal", "exact-after", "global-after", "stopping"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("order = %#v, want %#v", order, want)
	}
}
