package agentdefaultmodel

import (
	"errors"
	"strings"

	"github.com/gorenx/goren/agent"
)

// Config is the owner-defined boot configuration for the composition-backed
// default model selection.
type Config struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// ValidateConfig converts wire configuration into the runtime selection.
func ValidateConfig(settings Config) (agent.ModelSelection, error) {
	if strings.TrimSpace(settings.Provider) == "" ||
		settings.Provider != strings.TrimSpace(settings.Provider) ||
		strings.TrimSpace(settings.Model) == "" ||
		settings.Model != strings.TrimSpace(settings.Model) {
		return agent.ModelSelection{}, errors.New(
			"agentdefaultmodel: provider and model must be non-empty and trimmed",
		)
	}
	return agent.ModelSelection{
		Provider: settings.Provider,
		Model:    settings.Model,
	}, nil
}
