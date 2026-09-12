package systemprompt

// NewLayer constructs the root registration state without a framework lifecycle.
func NewLayer(settings ValidatedConfig) (*PromptLayer, error) {
	baseline := settings.definitions()
	owner := &PromptLayer{
		settings: settings,
		baseline: &baseline,
		store:    newPromptStore(),
	}
	return owner, nil
}

// DeriveLayer creates independent local definitions over a captured base.
func DeriveLayer(base Snapshot) *PromptLayer {
	return &PromptLayer{
		store: newPromptStore(),
		base:  base,
	}
}

// definitions derives configuration content without registering lifecycle effects.
func (input ValidatedConfig) definitions() promptLayerSnapshot {
	sections := make([]PromptSection, 0, 2)
	if input.includeHarnessIdentity {
		sections = append(sections, PromptSection{
			Name:  harnessIdentityName,
			Order: harnessIdentityOrder,
			Text:  StaticText(harnessIdentityText),
		})
	}
	sections = append(sections, PromptSection{
		Name:  PersonaSection,
		Order: PersonaOrder,
		Text:  StaticText(input.persona),
	})
	return promptLayerSnapshot{
		sections:          sections,
		contextSuppressed: !input.includeRuntimeContext,
	}
}
