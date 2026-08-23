package childscope

import (
	"context"

	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/systemprompt"
)

type persona struct {
	plugin.Base
	text   string
	handle *systemprompt.PromptHandle
}

func newPersona(text string) *persona {
	return &persona{
		text: text,
	}
}

func (*persona) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "@goren/subagent/persona",
		Requires: []plugin.ServiceType{
			plugin.ServiceOf[systemprompt.PromptRegistry](),
		},
	}
}

func (prompt *persona) Apply(requestContext context.Context) error {
	prompts, requireErr := plugin.Require[systemprompt.PromptRegistry](prompt)
	if requireErr != nil {
		return requireErr
	}
	handle, addErr := prompts.AddSection(
		requestContext,
		systemprompt.PromptSection{
			Name:     systemprompt.PersonaSection,
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

func (prompt *persona) Dispose(closeContext context.Context) error {
	if prompt.handle == nil {
		return nil
	}
	closeErr := prompt.handle.Unregister(closeContext)
	prompt.handle = nil
	return closeErr
}
