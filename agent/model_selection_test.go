package agent_test

import (
	"context"
	"testing"

	agentcore "github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/systemprompt"
)

func TestModelSelectionSnapshotsPromptAndRequestTogether(t *testing.T) {
	t.Parallel()
	promptConfig, err := systemprompt.ValidateConfig(
		systemprompt.Config{
			IncludeHarnessIdentity: boolPointer(false),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	promptRoot := systemprompt.New(
		promptConfig,
		systemprompt.RegistryOptions{},
	)
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	handles, err := runtimeEngine.Start(context.Background(), promptRoot)
	if err != nil {
		t.Fatal(err)
	}
	promptOverlay := systemprompt.NewOverlay(systemprompt.RegistryOptions{})
	overlayHandle, err := runtimeEngine.MountScopedChild(
		context.Background(),
		handles[0],
		promptOverlay,
	)
	if err != nil {
		t.Fatal(err)
	}
	subject := newFakeAgent(t, "model-selection")
	subject.runtime = &fakeScopeRuntime{
		source: subject,
	}
	subjectHandle, err := runtimeEngine.MountChild(
		context.Background(),
		overlayHandle,
		subject,
	)
	if err != nil {
		t.Fatal(err)
	}
	selection := &agentcore.ModelSelectionRef{}
	selectionPlugin, err := agentcore.NewModelSelectionPlugin(selection)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtimeEngine.MountChild(
		context.Background(),
		subjectHandle,
		selectionPlugin,
	); err != nil {
		t.Fatal(err)
	}
	selection.SetCurrent(&agentcore.ModelSelection{
		Provider:        "alpha",
		Model:           "a1",
		ReasoningEffort: llm.ReasoningEffortID("high"),
	})
	assembled, err := promptOverlay.Assemble(
		context.Background(),
		systemprompt.AssembleContext{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if assembled.Variables["provider"].Value != "alpha" ||
		assembled.Variables["model"].Value != "a1" {
		t.Fatalf("prompt variables = %#v", assembled.Variables)
	}
	selection.SetCurrent(&agentcore.ModelSelection{
		Provider: "beta",
		Model:    "b1",
	})
	resolved, err := agentcore.ResolveRequest(
		context.Background(),
		agentcore.RequestNotice{
			Subject: subject,
			Turn:    1,
			Step:    1,
		},
		requestResolutionAction{
			resolution: agentcore.RequestResolution{
				Config: llm.CallConfig{
					Provider:        "seed",
					Model:           "seed",
					ReasoningEffort: "max",
				},
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Provider != "alpha" || resolved.Model != "a1" ||
		resolved.ReasoningEffort != "high" {
		t.Fatalf("request selection = %#v", resolved)
	}
	if _, err := promptOverlay.Assemble(
		context.Background(),
		systemprompt.AssembleContext{},
	); err != nil {
		t.Fatal(err)
	}
	resolved, err = agentcore.ResolveRequest(
		context.Background(),
		agentcore.RequestNotice{
			Subject: subject,
			Turn:    1,
			Step:    2,
		},
		requestResolutionAction{
			resolution: agentcore.RequestResolution{
				Config: llm.CallConfig{
					Provider:        "seed",
					Model:           "seed",
					ReasoningEffort: "max",
				},
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Provider != "beta" || resolved.Model != "b1" ||
		resolved.ReasoningEffort != "" {
		t.Fatalf("second request selection = %#v", resolved)
	}
}

func TestModelSelectionReadsLiveFallbackUntilExplicitlySelected(t *testing.T) {
	t.Parallel()
	fallback := agentcore.ModelSelection{
		Provider: "first",
		Model:    "one",
	}
	selection := agentcore.NewModelSelectionRef(
		func() (agentcore.ModelSelection, bool, error) {
			return fallback, true, nil
		},
	)
	current, found, err := selection.Current()
	if err != nil || !found || current != fallback {
		t.Fatalf(
			"initial current = %#v, found = %t, error = %v",
			current,
			found,
			err,
		)
	}
	fallback = agentcore.ModelSelection{
		Provider: "second",
		Model:    "two",
	}
	current, found, err = selection.Current()
	if err != nil || !found || current != fallback {
		t.Fatalf(
			"live current = %#v, found = %t, error = %v",
			current,
			found,
			err,
		)
	}
	explicit := agentcore.ModelSelection{
		Provider: "chosen",
		Model:    "exact",
	}
	selection.SetCurrent(&explicit)
	fallback = agentcore.ModelSelection{
		Provider: "third",
		Model:    "three",
	}
	current, found, err = selection.Current()
	if err != nil || !found || current != explicit {
		t.Fatalf(
			"explicit current = %#v, found = %t, error = %v",
			current,
			found,
			err,
		)
	}
}

func boolPointer(selected bool) *bool { return &selected }
