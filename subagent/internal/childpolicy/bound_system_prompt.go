package childpolicy

import (
	"context"

	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/systemprompt"
)

const boundSystemPromptSection = "subagent:bound"

// boundSystemPrompt installs the complete model identity for a Bound child.
// It is not a Persona overlay and does not retain the parent prompt identity.
type boundSystemPrompt struct {
	plugin.Base
	text   string
	handle *systemprompt.PromptHandle
}

func newBoundSystemPrompt(text string) *boundSystemPrompt {
	return &boundSystemPrompt{
		text: text,
	}
}

func (*boundSystemPrompt) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "@goren/subagent/bound-system-prompt",
		Requires: []plugin.ServiceType{
			plugin.ServiceOf[systemprompt.PromptRegistry](),
		},
	}
}

func (prompt *boundSystemPrompt) Apply(
	requestContext context.Context,
) error {
	prompts, requireErr := plugin.Require[systemprompt.PromptRegistry](prompt)
	if requireErr != nil {
		return requireErr
	}
	handle, addErr := prompts.AddSection(
		requestContext,
		systemprompt.PromptSection{
			Name:     boundSystemPromptSection,
			Order:    systemprompt.PersonaOrder,
			Text:     systemprompt.StaticText(prompt.text),
			Complete: true,
		},
	)
	if addErr != nil {
		return addErr
	}
	prompt.handle = handle
	return nil
}

func (prompt *boundSystemPrompt) Dispose(
	closeContext context.Context,
) error {
	if prompt.handle == nil {
		return nil
	}
	closeErr := prompt.handle.Unregister(closeContext)
	prompt.handle = nil
	return closeErr
}
