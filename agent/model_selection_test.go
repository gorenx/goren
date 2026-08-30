package agent_test

import (
	"context"
	"testing"

	agentcore "github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/systemprompt"
)

type modelSelectionEditor struct {
	agentcore.ScopeEditor
	prompt  systemprompt.AssemblyMiddleware
	request agentcore.RequestMiddleware
}

func (editor *modelSelectionEditor) UsePromptAssembly(
	middleware systemprompt.AssemblyMiddleware,
) error {
	editor.prompt = middleware
	return nil
}

func (editor *modelSelectionEditor) UseRequest(
	middleware agentcore.RequestMiddleware,
) error {
	editor.request = middleware
	return nil
}

func (*modelSelectionEditor) Own(agentcore.ScopeResource) error { return nil }

type promptAssemblyAction struct{}

func (promptAssemblyAction) Execute(
	context.Context,
	systemprompt.AssembleRequest,
) (systemprompt.PromptAssembly, error) {
	return systemprompt.PromptAssembly{}, nil
}

func TestModelSelectionSnapshotsPromptAndRequestTogether(t *testing.T) {
	t.Parallel()
	subject := newRegistryAgent("model-selection")
	selection := &agentcore.ModelSelectionRef{}
	selectionSetup, err := agentcore.NewModelSelectionSetup(selection)
	if err != nil {
		t.Fatal(err)
	}
	editor := &modelSelectionEditor{}
	if err = selectionSetup.Apply(
		context.Background(),
		subject,
		editor,
	); err != nil {
		t.Fatal(err)
	}
	selection.SetCurrent(&agentcore.ModelSelection{
		Provider:        "alpha",
		Model:           "a1",
		ReasoningEffort: llm.ReasoningEffortID("high"),
	})
	assembled, err := editor.prompt.InterceptAssembly(
		context.Background(),
		systemprompt.AssembleRequest{},
		promptAssemblyAction{},
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
	resolution, err := editor.request.InterceptRequest(
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
	resolved := resolution.Config
	if resolved.Provider != "alpha" || resolved.Model != "a1" ||
		resolved.ReasoningEffort != "high" {
		t.Fatalf("request selection = %#v", resolved)
	}
	if _, err := editor.prompt.InterceptAssembly(
		context.Background(),
		systemprompt.AssembleRequest{},
		promptAssemblyAction{},
	); err != nil {
		t.Fatal(err)
	}
	resolution, err = editor.request.InterceptRequest(
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
	resolved = resolution.Config
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
