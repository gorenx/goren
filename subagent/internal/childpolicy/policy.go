// Package childpolicy maps durable Subagent policy to one child Agent Setup.
package childpolicy

import (
	"context"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/systemprompt"
	"github.com/gorenx/goren/tools"
)

const (
	boundSystemPromptSection = "subagent:bound"
	restrictionName          = "subagent"
)

// PolicySet describes child-local policy contributions.
type PolicySet struct {
	Persona         *string
	SystemPrompt    *string
	ToolRestriction *tools.ToolRestriction
}

// Setup returns one ordered child policy Setup, or nil when no policy applies.
func Setup(selected PolicySet) agent.Setup {
	if selected.Persona == nil && selected.SystemPrompt == nil &&
		selected.ToolRestriction == nil {
		return nil
	}
	return policySetup{
		selected: selected,
	}
}

type policySetup struct {
	selected PolicySet
}

func (contribution policySetup) Apply(
	requestContext context.Context,
	_ agent.Agent,
	editor agent.ScopeEditor,
) error {
	if contribution.selected.Persona != nil {
		if err := editor.AddPromptSection(
			requestContext,
			systemprompt.PromptSection{
				Name:     systemprompt.PersonaSection,
				Order:    systemprompt.PersonaOrder,
				Text:     systemprompt.StaticText(*contribution.selected.Persona),
				Complete: true,
			},
		); err != nil {
			return err
		}
	}
	if contribution.selected.SystemPrompt != nil {
		if err := editor.AddPromptSection(
			requestContext,
			systemprompt.PromptSection{
				Name:     boundSystemPromptSection,
				Order:    systemprompt.PersonaOrder,
				Text:     systemprompt.StaticText(*contribution.selected.SystemPrompt),
				Complete: true,
			},
		); err != nil {
			return err
		}
	}
	if contribution.selected.ToolRestriction != nil {
		if err := editor.AddToolRestriction(
			requestContext,
			restrictionName,
			*contribution.selected.ToolRestriction,
		); err != nil {
			return err
		}
	}
	return nil
}

var _ agent.Setup = policySetup{}
