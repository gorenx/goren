package llm

import (
	"context"
	"encoding/json"
	"slices"
)

// MessageID is the stable identity carried across inbox, Session, and model request boundaries.
type MessageID string

// ProviderRequestID is an opaque provider-issued diagnostic identity.
type ProviderRequestID string

// ReasoningEffortID is an adapter-owned model capability value.
type ReasoningEffortID string

// ModelModality is one provider-declared request modality.
type ModelModality string

const (
	// ModalityText identifies text request support.
	ModalityText ModelModality = "text"
	// ModalityImage identifies image request support.
	ModalityImage ModelModality = "image"
)

// TokenUsage contains disjoint request accounting. Cached input is excluded
// from InputTokens and reported by its own fields.
type TokenUsage struct {
	InputTokens      int64  `json:"inputTokens"`
	OutputTokens     int64  `json:"outputTokens"`
	CacheReadTokens  *int64 `json:"cacheReadTokens,omitempty"`
	CacheWriteTokens *int64 `json:"cacheWriteTokens,omitempty"`
	ReasoningTokens  *int64 `json:"reasoningTokens,omitempty"`
}

// ProviderInfo is detached selector metadata for one active provider route.
type ProviderInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ConfigurableProvider is one route an adapter plugin can activate through typed configuration.
type ConfigurableProvider struct {
	Provider     string   `json:"provider"`
	DisplayName  string   `json:"displayName"`
	SettingsNS   string   `json:"settingsNs"`
	SettingsPath []string `json:"settingsPath"`
	Declared     *bool    `json:"declared,omitempty"`
}

// ModelDiscoveryRequest describes a draft endpoint interrogation.
type ModelDiscoveryRequest struct {
	Provider string
	BaseURL  string
	API      string
	APIKey   string
}

// DiscoveredModel is candidate metadata reported by a provider endpoint.
type DiscoveredModel struct {
	ID            string `json:"id"`
	Name          string `json:"name,omitempty"`
	ContextWindow *int   `json:"contextWindow,omitempty"`
	MaxTokens     *int   `json:"maxTokens,omitempty"`
}

// ModelInfo is advisory model metadata from one active provider route.
type ModelInfo struct {
	Provider        string          `json:"provider"`
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	Description     string          `json:"description,omitempty"`
	InputModalities []ModelModality `json:"inputModalities,omitempty"`
}

// ModelContext contains a provider-owned combined request/response capacity.
type ModelContext struct {
	ContextWindow int `json:"contextWindow"`
}

// ReasoningEffortInfo is selector metadata for one exact model effort.
type ReasoningEffortInfo struct {
	ID          ReasoningEffortID `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
}

// ModelReasoningInfo is the complete selectable reasoning surface for one model.
type ModelReasoningInfo struct {
	Efforts       []ReasoningEffortInfo `json:"efforts"`
	DefaultEffort ReasoningEffortID     `json:"defaultEffort,omitempty"`
}

// ResolvedModelInfo is exact-route capability metadata detached from an adapter.
type ResolvedModelInfo struct {
	ModelInfo
	Context          *ModelContext       `json:"context,omitempty"`
	DefaultMaxTokens *int                `json:"defaultMaxTokens,omitempty"`
	Reasoning        *ModelReasoningInfo `json:"reasoning,omitempty"`
}

// CallPurpose classifies model-hidden auxiliary requests.
type CallPurpose string

const (
	// PurposeCompaction identifies a context-compaction request.
	PurposeCompaction CallPurpose = "compaction"
	// PurposeSessionTitle identifies a session-title request.
	PurposeSessionTitle CallPurpose = "session-title"
)

// CallConfig contains request-header state that affects provider routing and cache reuse.
type CallConfig struct {
	Provider        string
	Model           string
	ReasoningEffort ReasoningEffortID
	Temperature     *float64
	MaxTokens       *int
	Stop            []string
}

// CallConfigAdapterDefaults identifies fields materialized by exact-model resolution.
type CallConfigAdapterDefaults struct {
	ReasoningEffort bool
	MaxTokens       bool
}

// GenerateOptions is one fully assembled provider-neutral model request.
// Cancellation is supplied by the Stream context rather than retained inside this value.
type GenerateOptions struct {
	CallConfig
	Messages  []Message
	System    *string
	Tools     []ToolSchema
	SessionID string
	Purpose   CallPurpose
}

// ModelDiscovery resolves candidate metadata for a draft endpoint.
type ModelDiscovery interface {
	Discover(context.Context, ModelDiscoveryRequest) ([]DiscoveredModel, error)
}

// ModelDiscoveryFunc adapts a function to ModelDiscovery.
type ModelDiscoveryFunc func(context.Context, ModelDiscoveryRequest) ([]DiscoveredModel, error)

// Discover invokes the adapted function.
func (operation ModelDiscoveryFunc) Discover(requestContext context.Context, request ModelDiscoveryRequest) ([]DiscoveredModel, error) {
	return operation(requestContext, request)
}

func callConfigEqual(left CallConfig, right CallConfig) bool {
	return left.Provider == right.Provider && left.Model == right.Model &&
		left.ReasoningEffort == right.ReasoningEffort && equalFloat(left.Temperature, right.Temperature) &&
		equalInt(left.MaxTokens, right.MaxTokens) && (left.Stop == nil) == (right.Stop == nil) &&
		slices.Equal(left.Stop, right.Stop)
}

func cloneCallConfig(inputSnapshot CallConfig) CallConfig {
	detached := inputSnapshot
	detached.Temperature = cloneFloat(inputSnapshot.Temperature)
	detached.MaxTokens = cloneInt(inputSnapshot.MaxTokens)
	if inputSnapshot.Stop != nil {
		detached.Stop = append([]string{}, inputSnapshot.Stop...)
	}
	return detached
}

func cloneGenerateOptions(inputSnapshot GenerateOptions) (GenerateOptions, error) {
	detached := inputSnapshot
	detached.CallConfig = cloneCallConfig(inputSnapshot.CallConfig)
	conversation, err := CloneMessages(inputSnapshot.Messages)
	if err != nil {
		return GenerateOptions{}, err
	}
	detached.Messages = conversation
	if inputSnapshot.System != nil {
		prompt := *inputSnapshot.System
		detached.System = &prompt
	}
	detached.Tools = make([]ToolSchema, len(inputSnapshot.Tools))
	for index, entry := range inputSnapshot.Tools {
		detached.Tools[index] = entry
		detached.Tools[index].Parameters = append(json.RawMessage(nil), entry.Parameters...)
	}
	return detached, nil
}

func equalFloat(left *float64, right *float64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func equalInt(left *int, right *int) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func cloneInt64(inputSnapshot *int64) *int64 {
	if inputSnapshot == nil {
		return nil
	}
	value := *inputSnapshot
	return &value
}

func cloneUsage(inputSnapshot TokenUsage) TokenUsage {
	detached := inputSnapshot
	detached.CacheReadTokens = cloneInt64(inputSnapshot.CacheReadTokens)
	detached.CacheWriteTokens = cloneInt64(inputSnapshot.CacheWriteTokens)
	detached.ReasoningTokens = cloneInt64(inputSnapshot.ReasoningTokens)
	return detached
}
