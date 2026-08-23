package report

import (
	"context"
	"errors"
	"sync"

	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/systemprompt"
	"github.com/gorenx/goren/tools"
)

const reportPrompt = "Deliver your result with the report tool before you finish: call it once with a self-contained answer. The agent that started you does not automatically receive your transcript, tool output, or reasoning. Report earlier as well whenever a partial finding changes what that agent should do next; reporting never ends your turn."

type extension struct {
	continuations subagent.ContinuableService
	delivery      subagent.ReportDelivery
}

func (contribution *extension) Install(
	requestContext context.Context,
	activation subagent.ActivationContext,
) (subagent.Installation, error) {
	if activation.Agent == nil {
		return nil, errors.New("subagent report: Activation Agent is unavailable")
	}
	toolCatalog, requireErr := plugin.Require[tools.ToolCatalog](activation.Agent)
	if requireErr != nil {
		return nil, requireErr
	}
	prompts, requireErr := plugin.Require[systemprompt.PromptRegistry](activation.Agent)
	if requireErr != nil {
		return nil, requireErr
	}
	promptHandle, addErr := prompts.AddSection(
		requestContext,
		systemprompt.PromptSection{
			Name:  "tool:report",
			Order: 117,
			Text:  systemprompt.StaticText(reportPrompt),
		},
	)
	if addErr != nil {
		return nil, addErr
	}
	installed := &installation{
		promptHandle: promptHandle,
	}
	toolHandle, addErr := toolCatalog.AddTool(
		requestContext,
		contribution.definition(),
	)
	if addErr != nil {
		rollbackErr := installed.Uninstall(
			context.WithoutCancel(requestContext),
		)
		return nil, errors.Join(addErr, rollbackErr)
	}
	installed.toolHandle = toolHandle
	return installed, nil
}

type installation struct {
	once         sync.Once
	toolHandle   *tools.ToolHandle
	promptHandle *systemprompt.PromptHandle
	err          error
}

func (installed *installation) Uninstall(closeContext context.Context) error {
	if installed == nil {
		return nil
	}
	installed.once.Do(func() {
		if closeContext == nil {
			closeContext = context.Background()
		}
		if installed.toolHandle != nil {
			installed.err = errors.Join(
				installed.err,
				installed.toolHandle.Unregister(closeContext),
			)
		}
		if installed.promptHandle != nil {
			installed.err = errors.Join(
				installed.err,
				installed.promptHandle.Unregister(closeContext),
			)
		}
	})
	return installed.err
}

var _ subagent.ActivationExtension = (*extension)(nil)
var _ subagent.Installation = (*installation)(nil)
