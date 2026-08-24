package title

import (
	"context"

	"github.com/gorenx/goren/session"
)

// AutomaticMode controls which human prompts schedule provider generation.
type AutomaticMode string

const (
	AutomaticFirstPrompt AutomaticMode = "first-prompt"
	AutomaticAllPrompts  AutomaticMode = "all-prompts"
)

// ProviderRequest is one immutable title-generation revision. Cancellation is
// carried by the Generate context rather than stored in this value.
type ProviderRequest struct {
	Session  session.Context
	Messages []UserMessage
	Route    *ModelProvenance
}

// ProviderResult is untrusted provider output before service-owned validation.
type ProviderResult struct {
	Title       string
	MessageSeqs []int64
	Model       *ModelProvenance
}

// Provider is the sole optional asynchronous title implementation.
type Provider interface {
	ID() ProviderID
	AutomaticMode() AutomaticMode
	Generate(context.Context, ProviderRequest) (ProviderResult, error)
}
