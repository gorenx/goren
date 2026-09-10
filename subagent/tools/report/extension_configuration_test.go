package report

import (
	"context"
	"testing"

	"github.com/gorenx/goren/subagent"
)

func TestExtensionContributesReportPrompt(t *testing.T) {
	configuration := subagent.ChildConfiguration{}
	contribution := &extension{
		tool: &reportTool{},
	}
	if err := contribution.ConfigureChild(context.Background(), &configuration); err != nil {
		t.Fatal(err)
	}
	if len(configuration.PromptSections) != 1 ||
		configuration.PromptSections[0].Name != "tool:report" {
		t.Fatalf("sections = %#v", configuration.PromptSections)
	}
}

func TestExtensionRejectsIncompleteConfiguration(t *testing.T) {
	if err := (&extension{}).ConfigureChild(
		context.Background(),
		&subagent.ChildConfiguration{},
	); err == nil {
		t.Fatal("missing report Tool was accepted")
	}
	contribution := &extension{
		tool: &reportTool{},
	}
	if err := contribution.ConfigureChild(context.Background(), nil); err == nil {
		t.Fatal("nil child configuration was accepted")
	}
}
