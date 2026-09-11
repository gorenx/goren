package llm

import (
	"errors"
	"math"
	"strings"
)

// Configuration retains each Agent's submitted call settings. It does not own
// global defaults or live calls. Its owner serializes access; reads are detached.
type Configuration struct {
	agents map[string]CallConfig
}

// Set replaces one Agent's entire configuration after validating the proposal.
func (settings *Configuration) Set(agentID string, proposed CallConfig) error {
	if strings.TrimSpace(agentID) == "" {
		return errors.New("llm: configuration requires an Agent identity")
	}
	if err := ValidateCallConfig(proposed); err != nil {
		return err
	}
	if settings.agents == nil {
		settings.agents = make(map[string]CallConfig)
	}
	settings.agents[agentID] = CloneCallConfig(proposed)
	return nil
}

// Get returns the last committed settings, never another Agent's defaults.
func (settings *Configuration) Get(agentID string) (CallConfig, error) {
	stored, found := settings.agents[agentID]
	if !found {
		return CallConfig{}, errors.New("llm: Agent configuration is not registered")
	}
	return CloneCallConfig(stored), nil
}

// Remove releases configuration only; the caller first closes the Agent's calls.
func (settings *Configuration) Remove(agentID string) {
	delete(settings.agents, agentID)
}

// ValidateCallConfig checks provider-neutral settings without selecting an
// adapter. Exact model capabilities and defaults are resolved inside a call.
func ValidateCallConfig(proposed CallConfig) error {
	if proposed.Provider == "" || strings.TrimSpace(proposed.Provider) != proposed.Provider || proposed.Model == "" || strings.TrimSpace(proposed.Model) != proposed.Model {
		return errors.New("llm: provider and model must be non-empty and trimmed")
	}
	if proposed.MaxTokens != nil && *proposed.MaxTokens <= 0 {
		return errors.New("llm: max tokens must be positive")
	}
	if proposed.Temperature != nil && (math.IsNaN(*proposed.Temperature) || math.IsInf(*proposed.Temperature, 0)) {
		return errors.New("llm: temperature must be finite")
	}
	return nil
}
