package modelconfig

import (
	"errors"
	"fmt"
	"strings"

	"github.com/gorenx/goren/credentials"
	"github.com/gorenx/goren/llm"
)

// Catalog owns the current validated model configuration. Its caller serializes access.
type Catalog struct {
	current Snapshot
}

// NewCatalog validates the initial definition before making version one available.
func NewCatalog(initial Definition) (*Catalog, error) {
	if err := validate(initial); err != nil {
		return nil, err
	}
	return &Catalog{
		current: Snapshot{
			Version:    1,
			Definition: cloneDefinition(initial),
		},
	}, nil
}

// Current returns a detached copy of the current configuration.
func (owner *Catalog) Current() Snapshot {
	return Snapshot{
		Version:    owner.current.Version,
		Definition: cloneDefinition(owner.current.Definition),
	}
}

// Read rejects obsolete versions instead of silently substituting current values.
func (owner *Catalog) Read(version uint64) (Snapshot, error) {
	if version != 0 && version != owner.current.Version {
		return Snapshot{}, ErrVersion
	}
	return owner.Current(), nil
}

// Replace validates the entire candidate before publishing the next version.
func (owner *Catalog) Replace(expected uint64, candidate Definition) (Snapshot, error) {
	if expected != owner.current.Version {
		return Snapshot{}, ErrVersion
	}
	if err := validate(candidate); err != nil {
		return Snapshot{}, err
	}
	if expected == ^uint64(0) {
		return Snapshot{}, errors.New("modelconfig: version exhausted")
	}
	owner.current = Snapshot{
		Version:    expected + 1,
		Definition: cloneDefinition(candidate),
	}
	return owner.Current(), nil
}

// Resolve combines deployment defaults with an Agent's explicit overrides.
func (owner *Catalog) Resolve(version uint64, overrides llm.CallConfig) (Resolved, error) {
	view, err := owner.Read(version)
	if err != nil {
		return Resolved{}, err
	}
	effective := llm.CloneCallConfig(view.Definition.Default)
	if overrides.Provider != "" {
		if overrides.Model == "" {
			return Resolved{}, errors.New("modelconfig: provider override requires model")
		}
		effective.Provider = overrides.Provider
	}
	if overrides.Model != "" {
		if effective.Provider != view.Definition.Default.Provider || overrides.Model != effective.Model {
			effective.ReasoningEffort = ""
		}
		effective.Model = overrides.Model
	}
	if overrides.ReasoningEffort != "" {
		effective.ReasoningEffort = overrides.ReasoningEffort
	}
	detached := llm.CloneCallConfig(overrides)
	if detached.Temperature != nil {
		effective.Temperature = detached.Temperature
	}
	if detached.MaxTokens != nil {
		effective.MaxTokens = detached.MaxTokens
	}
	if detached.Stop != nil {
		effective.Stop = detached.Stop
	}
	if err := validateCall(effective); err != nil {
		return Resolved{}, err
	}
	for _, entry := range view.Definition.Providers {
		if entry.ID == effective.Provider {
			return Resolved{
				Version:       view.Version,
				Config:        effective,
				CredentialRef: entry.CredentialRef,
			}, nil
		}
	}
	return Resolved{}, fmt.Errorf("modelconfig: unknown provider %q", effective.Provider)
}

func validate(candidate Definition) error {
	if err := validateCall(candidate.Default); err != nil {
		return err
	}
	seen := make(map[string]bool, len(candidate.Providers))
	for _, entry := range candidate.Providers {
		if entry.ID == "" || strings.TrimSpace(entry.ID) != entry.ID {
			return errors.New("modelconfig: invalid provider ID")
		}
		if seen[entry.ID] {
			return fmt.Errorf("modelconfig: duplicate provider %q", entry.ID)
		}
		seen[entry.ID] = true
		if entry.CredentialRef != "" {
			if err := credentials.ValidateRef(entry.CredentialRef); err != nil {
				return err
			}
		}
	}
	if !seen[candidate.Default.Provider] {
		return errors.New("modelconfig: default provider is not defined")
	}
	return nil
}

func validateCall(proposed llm.CallConfig) error {
	if proposed.Provider == "" || strings.TrimSpace(proposed.Provider) != proposed.Provider || proposed.Model == "" || strings.TrimSpace(proposed.Model) != proposed.Model {
		return errors.New("modelconfig: provider and model must be non-empty and trimmed")
	}
	if proposed.MaxTokens != nil && *proposed.MaxTokens <= 0 {
		return errors.New("modelconfig: max tokens must be positive")
	}
	return nil
}

func cloneDefinition(original Definition) Definition {
	return Definition{
		Default:   llm.CloneCallConfig(original.Default),
		Providers: append([]Provider(nil), original.Providers...),
	}
}
