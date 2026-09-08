package agent

import "errors"

// Options contains the provider-neutral configuration applied to one Agent.
type Options struct {
	Provider  string
	Model     string
	MaxTokens *int
}

// Clone copies mutable fields without changing unset option semantics.
func (settings Options) Clone() Options {
	if settings.MaxTokens != nil {
		limit := *settings.MaxTokens
		settings.MaxTokens = &limit
	}
	return settings
}

// Validate checks Agent-owned numeric constraints. Model availability and
// provider-specific parameter validation belong to the LLM owner.
func (settings Options) Validate() error {
	if settings.MaxTokens != nil && (*settings.MaxTokens <= 0 || int64(*settings.MaxTokens) > int64(1<<53-1)) {
		return errors.New("agent: maxTokens must be a positive safe integer")
	}
	return nil
}
