// Package childpolicy maps durable Subagent policy into detached child
// configuration and Session seed facts.
package childpolicy

import (
	"context"
	"slices"

	"github.com/gorenx/goren/approval"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/systemprompt"
	toolregistry "github.com/gorenx/goren/tools/registry"
)

const boundSystemPromptSection = "subagent:bound"

type PolicySet struct {
	Persona         *string
	SystemPrompt    *string
	ToolRestriction *toolregistry.ToolRestriction
}

// Apply appends child-local values to an unpublished configuration.
func Apply(selected PolicySet, configuration *subagent.ChildConfiguration) {
	if selected.Persona != nil {
		configuration.PromptSections = append(configuration.PromptSections, systemprompt.PromptSection{
			Name:     systemprompt.PersonaSection,
			Order:    systemprompt.PersonaOrder,
			Text:     systemprompt.StaticText(*selected.Persona),
			Complete: true,
		})
	}
	if selected.SystemPrompt != nil {
		configuration.PromptSections = append(configuration.PromptSections, systemprompt.PromptSection{
			Name:     boundSystemPromptSection,
			Order:    systemprompt.PersonaOrder,
			Text:     systemprompt.StaticText(*selected.SystemPrompt),
			Complete: true,
		})
	}
	if selected.ToolRestriction != nil {
		detached := selected.ToolRestriction.Clone()
		configuration.Tools.Restriction = &toolregistry.ToolRestriction{
			Allow: slices.Clone(detached.Allow),
			Deny:  slices.Clone(detached.Deny),
		}
	}
}

// AppendDelegation records the delegated approval override in a fresh seed.
func AppendDelegation(seed []session.Event) ([]session.Event, error) {
	log, err := session.NewLog("subagent-policy", session.CreateOptions{Seed: seed})
	if err != nil {
		return nil, err
	}
	draft, err := approval.DelegationPolicyDraft()
	if err != nil {
		return nil, err
	}
	if _, err = log.Commit(context.Background(), session.Batch(draft)); err != nil {
		return nil, err
	}
	return log.Events(), nil
}
