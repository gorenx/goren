// Package llmdeepseek implements the direct DeepSeek chat-completions provider adapter.
package deepseek

import (
	"time"

	"github.com/gorenx/goren/llm"
)

const (
	ProviderRoute              = "deepseek-official"
	SettingsNamespace          = "llm-deepseek"
	DefaultAPIKeyEnv           = "DEEPSEEK_API_KEY"
	DefaultModelID             = "deepseek-v4-flash"
	BaseURLEnv                 = "DEEPSEEK_BASE_URL"
	PublicBaseURL              = "https://api.deepseek.com"
	DefaultContextWindow       = 1_000_000
	DefaultMaxTokens           = 256_000
	DefaultStreamIdleTimeoutMS = 300_000
	maxJavaScriptSafeInteger   = 9_007_199_254_740_991
)

type ThinkingMode string

const (
	ThinkingEnabled  ThinkingMode = "enabled"
	ThinkingDisabled ThinkingMode = "disabled"
)

type ReasoningEffort string

const (
	ReasoningOff  ReasoningEffort = "off"
	ReasoningHigh ReasoningEffort = "high"
	ReasoningMax  ReasoningEffort = "max"
)

// CatalogModel is one advisory model advertised by the direct adapter.
type CatalogModel struct {
	ID            string  `json:"id"`
	Name          *string `json:"name,omitempty"`
	Description   *string `json:"description,omitempty"`
	ContextWindow *int    `json:"contextWindow,omitempty"`
	MaxTokens     *int    `json:"maxTokens,omitempty"`
}

// Config is the strict owner-defined DeepSeek provider configuration.
// Credentials are references only; literal API keys are never configuration.
type Config struct {
	APIKeyEnv            *string                `json:"apiKeyEnv,omitempty"`
	BaseURL              *string                `json:"baseURL,omitempty"`
	Thinking             *ThinkingMode          `json:"thinking,omitempty"`
	ReasoningEffort      *ReasoningEffort       `json:"reasoningEffort,omitempty"`
	MaxTokens            *int                   `json:"maxTokens,omitempty"`
	DefaultContextWindow *int                   `json:"defaultContextWindow,omitempty"`
	Models               *[]CatalogModel        `json:"models,omitempty"`
	StreamIdleTimeoutMS  *float64               `json:"streamIdleTimeoutMs,omitempty"`
	RetryPolicy          *llm.RetryPolicyConfig `json:"retryPolicy,omitempty"`
}

// RequestDefaults contains adapter-owned thinking defaults.
type RequestDefaults struct {
	Thinking        *ThinkingMode
	ReasoningEffort *ReasoningEffort
}

// ConnectionOptions is one validated request-generation snapshot.
type ConnectionOptions struct {
	APIKeyEnv            string
	BaseURL              string
	Defaults             RequestDefaults
	MaxTokens            int
	DefaultContextWindow int
	Models               []CatalogModel
	StreamIdleTimeout    time.Duration
	RetryPolicy          llm.RetryPolicy
}

// LaunchEnvironment supplies the trusted launch layer consulted by static
// DeepSeek configuration resolution.
type LaunchEnvironment interface {
	Lookup(string) (string, bool)
}

// Snapshot returns a detached request-generation value.
func (connection ConnectionOptions) Snapshot() ConnectionOptions {
	detached := connection
	detached.Defaults = RequestDefaults{
		Thinking:        cloneThinking(connection.Defaults.Thinking),
		ReasoningEffort: cloneReasoningEffort(connection.Defaults.ReasoningEffort),
	}
	detached.Models = make([]CatalogModel, len(connection.Models))
	for index, catalogEntry := range connection.Models {
		detached.Models[index] = catalogEntry
		detached.Models[index].Name = cloneString(catalogEntry.Name)
		detached.Models[index].Description = cloneString(catalogEntry.Description)
		detached.Models[index].ContextWindow = cloneInt(catalogEntry.ContextWindow)
		detached.Models[index].MaxTokens = cloneInt(catalogEntry.MaxTokens)
	}
	if connection.RetryPolicy != nil {
		detached.RetryPolicy = connection.RetryPolicy.CloneRetryPolicy()
	}
	return detached
}

func cloneThinking(original *ThinkingMode) *ThinkingMode {
	if original == nil {
		return nil
	}
	value := *original
	return &value
}

func cloneReasoningEffort(original *ReasoningEffort) *ReasoningEffort {
	if original == nil {
		return nil
	}
	value := *original
	return &value
}

func cloneInt(original *int) *int {
	if original == nil {
		return nil
	}
	value := *original
	return &value
}

func intPointer(value int) *int { return &value }

func stringPointer(value string) *string { return &value }

func cloneString(original *string) *string {
	if original == nil {
		return nil
	}
	value := *original
	return &value
}
