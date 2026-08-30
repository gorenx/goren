package report

import (
	"context"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/systemprompt"
	"github.com/gorenx/goren/tools"
)

type extensionEditor struct {
	agent.ScopeEditor
	sections []systemprompt.PromptSection
	tools    []tools.ToolDefinition
}

func (editor *extensionEditor) AddPromptSection(
	_ context.Context,
	section systemprompt.PromptSection,
) error {
	editor.sections = append(editor.sections, section)
	return nil
}

func (editor *extensionEditor) AddTool(
	_ context.Context,
	toolDefinition tools.ToolDefinition,
) error {
	editor.tools = append(editor.tools, toolDefinition)
	return nil
}

func TestExtensionContributesReportPromptAndTool(t *testing.T) {
	t.Parallel()
	editor := &extensionEditor{}
	contribution := &extension{
		tool: &reportTool{},
	}
	if err := contribution.Apply(context.Background(), nil, editor); err != nil {
		t.Fatal(err)
	}
	if len(editor.sections) != 1 || editor.sections[0].Name != "tool:report" {
		t.Fatalf("sections = %#v", editor.sections)
	}
	if len(editor.tools) != 1 || editor.tools[0].Name != "report" {
		t.Fatalf("tools = %#v", editor.tools)
	}
}

func TestExtensionRejectsMissingReportTool(t *testing.T) {
	t.Parallel()
	if err := (&extension{}).Apply(
		context.Background(),
		nil,
		&extensionEditor{},
	); err == nil {
		t.Fatal("missing report Tool was accepted")
	}
}

var _ agent.ScopeEditor = (*extensionEditor)(nil)
