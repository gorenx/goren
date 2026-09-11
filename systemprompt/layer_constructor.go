package systemprompt

// NewLayer constructs the root registration state without a framework lifecycle.
func NewLayer(settings ValidatedConfig) (*PromptLayer, error) {
	owner := &PromptLayer{
		settings: settings,
		store:    newPromptStore(),
	}
	if err := owner.initialize(); err != nil {
		return nil, err
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

func (owner *PromptLayer) initialize() error {
	if owner.settings.includeHarnessIdentity {
		identifier, err := owner.store.addSection(PromptSection{
			Name:  harnessIdentityName,
			Order: harnessIdentityOrder,
			Text:  StaticText(harnessIdentityText),
		})
		if err != nil {
			return err
		}
		owner.configEntries = append(owner.configEntries, identifier)
	}
	identifier, err := owner.store.addSection(PromptSection{
		Name:  PersonaSection,
		Order: PersonaOrder,
		Text:  StaticText(owner.settings.persona),
	})
	if err != nil {
		return err
	}
	owner.configEntries = append(owner.configEntries, identifier)
	if !owner.settings.includeRuntimeContext {
		identifier, err := owner.store.addSuppressor(disabledRuntimeContextSuppressor)
		if err != nil {
			return err
		}
		owner.configEntries = append(owner.configEntries, identifier)
	}
	return nil
}
