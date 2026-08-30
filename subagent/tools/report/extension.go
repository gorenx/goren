package report

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/systemprompt"
)

const reportPrompt = "Deliver your result with the report tool before you finish: call it once with a self-contained answer. The agent that started you does not automatically receive your transcript, tool output, or reasoning. Report earlier as well whenever a partial finding changes what that agent should do next; reporting never ends your turn."

// extension contributes the report prompt and Tool to one child Agent Scope.
type extension struct {
	tool *reportTool
}

func (contribution *extension) Apply(
	requestContext context.Context,
	_ agent.Agent,
	editor agent.ScopeEditor,
) error {
	if contribution == nil || contribution.tool == nil {
		return errors.New("subagent report: delivery Tool is unavailable")
	}
	if err := editor.AddPromptSection(
		requestContext,
		systemprompt.PromptSection{
			Name:  "tool:report",
			Order: 117,
			Text:  systemprompt.StaticText(reportPrompt),
		},
	); err != nil {
		return err
	}
	return editor.AddTool(requestContext, contribution.tool.definition())
}

var _ subagent.Extension = (*extension)(nil)
