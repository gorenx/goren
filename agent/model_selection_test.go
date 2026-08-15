package agent_test

import (
	"context"
	"testing"

	agentcore "github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/systemprompt"
)

type modelSelectionPlugin struct {
	ready func(agentcore.Registry, systemprompt.SystemPrompt, *plugin.Scope) error
}

func (modelSelectionPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{Name: "agent-model-selection-fixture", Provides: []plugin.ServiceRef{
		agentcore.Service.Ref(), systemprompt.Service.Ref(),
	}}
}

func (instance modelSelectionPlugin) Apply(requestContext context.Context, pluginScope *plugin.Scope) error {
	agentService, err := agentcore.NewRegistry(pluginScope, agentcore.RegistryOptions{})
	if err != nil {
		return err
	}
	promptConfig, err := systemprompt.ValidateConfig(systemprompt.Config{IncludeHarnessIdentity: boolPointer(false)})
	if err != nil {
		return err
	}
	promptService, err := systemprompt.New(requestContext, pluginScope, promptConfig)
	if err != nil {
		return err
	}
	if _, err := plugin.Provide(pluginScope, agentcore.Service, agentService); err != nil {
		return err
	}
	if _, err := plugin.Provide(pluginScope, systemprompt.Service, promptService); err != nil {
		return err
	}
	return instance.ready(agentService, promptService, pluginScope)
}

func TestModelSelectionSnapshotsPromptAndRequestTogether(t *testing.T) {
	t.Parallel()
	engine := plugin.NewRuntime()
	selection := &agentcore.ModelSelectionRef{}
	var promptService systemprompt.SystemPrompt
	var providerScope *plugin.Scope
	_, err := engine.Load(context.Background(), modelSelectionPlugin{ready: func(_ agentcore.Registry, available systemprompt.SystemPrompt, pluginScope *plugin.Scope) error {
		promptService = available
		providerScope = pluginScope
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	subject, _ := newFakeAgent(t, providerScope, "model-selection")
	if _, err := agentcore.InstallModelSelection(subject.ScopeValue(), selection); err != nil {
		t.Fatal(err)
	}
	selection.SetCurrent(&agentcore.ModelSelection{
		Provider: "alpha", Model: "a1", ReasoningEffort: llm.ReasoningEffortID("high"),
	})
	assembled, err := promptService.Assemble(context.Background(), systemprompt.AssembleContext{Scope: subject.ScopeValue().Target()})
	if err != nil {
		t.Fatal(err)
	}
	if assembled.Variables["provider"].Value != "alpha" || assembled.Variables["model"].Value != "a1" {
		t.Fatalf("prompt variables = %#v", assembled.Variables)
	}
	selection.SetCurrent(&agentcore.ModelSelection{Provider: "beta", Model: "b1"})
	resolved, err := agentcore.ResolveRequest(context.Background(), providerScope, agentcore.RequestNotice{
		Subject: subject, Turn: 1, Step: 1,
	}, func(context.Context) (llm.CallConfig, error) {
		return llm.CallConfig{Provider: "seed", Model: "seed", ReasoningEffort: "max"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Provider != "alpha" || resolved.Model != "a1" || resolved.ReasoningEffort != "high" {
		t.Fatalf("request selection = %#v", resolved)
	}
	if _, err := promptService.Assemble(context.Background(), systemprompt.AssembleContext{Scope: subject.ScopeValue().Target()}); err != nil {
		t.Fatal(err)
	}
	resolved, err = agentcore.ResolveRequest(context.Background(), providerScope, agentcore.RequestNotice{
		Subject: subject, Turn: 1, Step: 2,
	}, func(context.Context) (llm.CallConfig, error) {
		return llm.CallConfig{Provider: "seed", Model: "seed", ReasoningEffort: "max"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Provider != "beta" || resolved.Model != "b1" || resolved.ReasoningEffort != "" {
		t.Fatalf("second request selection = %#v", resolved)
	}
}

func boolPointer(selected bool) *bool { return &selected }
