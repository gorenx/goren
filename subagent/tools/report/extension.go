package report

import (
	"context"
	"errors"

	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/systemprompt"
)

const reportPrompt = "Deliver your result with the report tool before you finish: call it once with a self-contained answer. The agent that started you does not automatically receive your transcript, tool output, or reasoning. Report earlier as well whenever a partial finding changes what that agent should do next; reporting never ends your turn."

// Section supplies the reporting instructions to ordinary Agent prompt assembly.
func Section() systemprompt.PromptSection {
	return systemprompt.PromptSection{
		Name:  "tool:report",
		Order: 117,
		Text:  systemprompt.StaticText(reportPrompt),
	}
}

// extension contributes the report prompt and Tool to one child Agent Scope.
type extension struct {
	tool *reportTool
}

func (contribution *extension) ConfigureChild(
	requestContext context.Context,
	configuration *subagent.ChildConfiguration,
) error {
	if contribution == nil || contribution.tool == nil || configuration == nil {
		return errors.New("subagent report: delivery Tool is unavailable")
	}
	if err := context.Cause(requestContext); err != nil {
		return err
	}
	configuration.PromptSections = append(
		configuration.PromptSections,
		Section(),
	)
	return nil
}

var _ subagent.Extension = (*extension)(nil)
