package agentdefaultmodel_test

import (
	"context"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentdefaultmodel"
)

func TestStaticDefaultReturnsCompositionSelection(t *testing.T) {
	t.Parallel()
	defaults, err := agentdefaultmodel.NewStatic(agent.ModelSelection{Provider: "provider", Model: "model"})
	if err != nil {
		t.Fatal(err)
	}
	if selected := defaults.CurrentSelection(); selected.Provider != "provider" || selected.Model != "model" {
		t.Fatalf("selection = %#v", selected)
	}
	if err := defaults.SaveSelection(context.Background(), agent.ModelSelection{Provider: "other", Model: "other"}); err != nil {
		t.Fatal(err)
	}
	if selected := defaults.CurrentSelection(); selected.Provider != "provider" || selected.Model != "model" {
		t.Fatalf("selection after absent Settings save = %#v", selected)
	}
}

func TestStaticDefaultRejectsIncompleteSelection(t *testing.T) {
	t.Parallel()
	if _, err := agentdefaultmodel.NewStatic(agent.ModelSelection{Provider: "provider"}); err == nil {
		t.Fatal("incomplete default selection was accepted")
	}
}
