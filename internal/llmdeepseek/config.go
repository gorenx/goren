// Package llmdeepseek implements the direct DeepSeek chat-completions provider adapter.
package llmdeepseek

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/gorenx/goren/llm"
)

const (
	ProviderRoute              = "deepseek-official"
	SettingsNamespace          = "llm-deepseek"
	DefaultAPIKeyEnv           = "DEEPSEEK_API_KEY"
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

// Snapshot returns a detached request-generation value.
func (source ConnectionOptions) Snapshot() ConnectionOptions {
	detached := source
	detached.Defaults = RequestDefaults{
		Thinking:        cloneThinking(source.Defaults.Thinking),
		ReasoningEffort: cloneReasoningEffort(source.Defaults.ReasoningEffort),
	}
	detached.Models = make([]CatalogModel, len(source.Models))
	for index, catalogEntry := range source.Models {
		detached.Models[index] = catalogEntry
		detached.Models[index].Name = cloneString(catalogEntry.Name)
		detached.Models[index].Description = cloneString(catalogEntry.Description)
		detached.Models[index].ContextWindow = cloneInt(catalogEntry.ContextWindow)
		detached.Models[index].MaxTokens = cloneInt(catalogEntry.MaxTokens)
	}
	if source.RetryPolicy != nil {
		detached.RetryPolicy = source.RetryPolicy.CloneRetryPolicy()
	}
	return detached
}

// Environment supplies the trusted launch layer consulted by static config resolution.
type Environment struct {
	LookupEnv func(string) (string, bool)
}

// ResolveOptions validates one whole connection generation atomically.
func ResolveOptions(settings Config, launchEnvironment Environment) (ConnectionOptions, error) {
	if settings.Thinking != nil {
		if *settings.Thinking != ThinkingEnabled && *settings.Thinking != ThinkingDisabled {
			return ConnectionOptions{}, errors.New("llm-deepseek: thinking must be enabled or disabled")
		}
	}
	if settings.ReasoningEffort != nil {
		switch *settings.ReasoningEffort {
		case ReasoningOff, ReasoningHigh, ReasoningMax:
		default:
			return ConnectionOptions{}, errors.New("llm-deepseek: reasoningEffort must be off, high, or max")
		}
	}
	if settings.Thinking != nil && *settings.Thinking == ThinkingDisabled &&
		settings.ReasoningEffort != nil && *settings.ReasoningEffort != ReasoningOff {
		return ConnectionOptions{}, errors.New("llm-deepseek: only reasoningEffort off can be configured when thinking is disabled")
	}
	credentialRef := DefaultAPIKeyEnv
	if settings.APIKeyEnv != nil {
		credentialRef = *settings.APIKeyEnv
	}
	if strings.TrimSpace(credentialRef) == "" || credentialRef != strings.TrimSpace(credentialRef) {
		return ConnectionOptions{}, errors.New("llm-deepseek: apiKeyEnv must be non-empty and trimmed")
	}
	baseURL := ""
	if settings.BaseURL != nil {
		baseURL = *settings.BaseURL
	} else if launchEnvironment.LookupEnv != nil {
		baseURL, _ = launchEnvironment.LookupEnv(BaseURLEnv)
	}
	if baseURL == "" {
		baseURL = PublicBaseURL
	}
	maximumTokens := DefaultMaxTokens
	if settings.MaxTokens != nil {
		maximumTokens = *settings.MaxTokens
	}
	if maximumTokens <= 0 || int64(maximumTokens) > maxJavaScriptSafeInteger {
		return ConnectionOptions{}, errors.New("llm-deepseek: maxTokens must be a positive safe integer")
	}
	contextWindow := DefaultContextWindow
	if settings.DefaultContextWindow != nil {
		contextWindow = *settings.DefaultContextWindow
	}
	if contextWindow <= 0 {
		return ConnectionOptions{}, errors.New("llm-deepseek: defaultContextWindow must be a positive integer")
	}
	modelCatalog, err := resolveModels(settings.Models)
	if err != nil {
		return ConnectionOptions{}, err
	}
	idleMilliseconds := float64(DefaultStreamIdleTimeoutMS)
	if settings.StreamIdleTimeoutMS != nil {
		idleMilliseconds = *settings.StreamIdleTimeoutMS
	}
	if math.IsNaN(idleMilliseconds) || math.IsInf(idleMilliseconds, 0) ||
		idleMilliseconds <= 0 || idleMilliseconds > llm.MaxTimerDelayMS {
		return ConnectionOptions{}, fmt.Errorf(
			"llm-deepseek: streamIdleTimeoutMs must be positive and no greater than %.0f", llm.MaxTimerDelayMS,
		)
	}
	policy, err := llm.ResolveRetryPolicy(settings.RetryPolicy, "llm-deepseek: retryPolicy")
	if err != nil {
		return ConnectionOptions{}, err
	}
	return ConnectionOptions{
		APIKeyEnv: credentialRef, BaseURL: baseURL,
		Defaults:  RequestDefaults{Thinking: cloneThinking(settings.Thinking), ReasoningEffort: cloneReasoningEffort(settings.ReasoningEffort)},
		MaxTokens: maximumTokens, DefaultContextWindow: contextWindow, Models: modelCatalog,
		StreamIdleTimeout: time.Duration(idleMilliseconds * float64(time.Millisecond)), RetryPolicy: policy,
	}, nil
}

func resolveModels(configured *[]CatalogModel) ([]CatalogModel, error) {
	models := []CatalogModel{
		{ID: "deepseek-v4-flash", Name: stringPointer("DeepSeek-V4-Flash"), ContextWindow: intPointer(DefaultContextWindow)},
		{ID: "deepseek-v4-pro", Name: stringPointer("DeepSeek-V4-Pro"), ContextWindow: intPointer(DefaultContextWindow)},
	}
	if configured != nil {
		models = slices.Clone(*configured)
	}
	seen := make(map[string]struct{}, len(models))
	for index := range models {
		candidate := models[index]
		if candidate.ID == "" {
			return nil, errors.New("llm-deepseek: catalog model ids must be non-empty")
		}
		if candidate.Name != nil && *candidate.Name == "" {
			return nil, fmt.Errorf("llm-deepseek: catalog model %q has an empty name", candidate.ID)
		}
		if candidate.ContextWindow != nil && *candidate.ContextWindow <= 0 {
			return nil, fmt.Errorf("llm-deepseek: catalog model %q contextWindow must be a positive integer", candidate.ID)
		}
		if candidate.MaxTokens != nil && *candidate.MaxTokens <= 0 {
			return nil, fmt.Errorf("llm-deepseek: catalog model %q maxTokens must be a positive integer", candidate.ID)
		}
		if _, exists := seen[candidate.ID]; exists {
			return nil, fmt.Errorf("llm-deepseek: duplicate catalog model %q", candidate.ID)
		}
		seen[candidate.ID] = struct{}{}
		candidate.ContextWindow = cloneInt(candidate.ContextWindow)
		candidate.MaxTokens = cloneInt(candidate.MaxTokens)
		candidate.Name = cloneString(candidate.Name)
		candidate.Description = cloneString(candidate.Description)
		models[index] = candidate
	}
	return models, nil
}

type configWire struct {
	APIKeyEnv            json.RawMessage `json:"apiKeyEnv"`
	BaseURL              json.RawMessage `json:"baseURL"`
	Thinking             json.RawMessage `json:"thinking"`
	ReasoningEffort      json.RawMessage `json:"reasoningEffort"`
	MaxTokens            json.RawMessage `json:"maxTokens"`
	DefaultContextWindow json.RawMessage `json:"defaultContextWindow"`
	Models               json.RawMessage `json:"models"`
	StreamIdleTimeoutMS  json.RawMessage `json:"streamIdleTimeoutMs"`
	RetryPolicy          json.RawMessage `json:"retryPolicy"`
}

type catalogModelWire struct {
	ID            json.RawMessage `json:"id"`
	Name          json.RawMessage `json:"name"`
	Description   json.RawMessage `json:"description"`
	ContextWindow json.RawMessage `json:"contextWindow"`
	MaxTokens     json.RawMessage `json:"maxTokens"`
}

// UnmarshalJSON keeps optional catalog fields distinguishable from explicit
// null or empty values and rejects ownerless extension keys.
func (catalogEntry *CatalogModel) UnmarshalJSON(encoded []byte) error {
	if catalogEntry == nil {
		return errors.New("llm-deepseek: cannot decode catalog model into nil target")
	}
	if isNull(encoded) {
		return errors.New("llm-deepseek: catalog model must be an object")
	}
	var wireValue catalogModelWire
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wireValue); err != nil {
		return err
	}
	if err := requireJSONEOF(decoder); err != nil {
		return err
	}
	if len(wireValue.ID) == 0 || isNull(wireValue.ID) {
		return errors.New("llm-deepseek: catalog model id is required")
	}
	var decoded CatalogModel
	if err := json.Unmarshal(wireValue.ID, &decoded.ID); err != nil {
		return errors.New("llm-deepseek: catalog model id must be a string")
	}
	if err := decodeOptional(wireValue.Name, "catalog model name", &decoded.Name); err != nil {
		return err
	}
	if err := decodeOptional(wireValue.Description, "catalog model description", &decoded.Description); err != nil {
		return err
	}
	if err := decodeOptional(wireValue.ContextWindow, "catalog model contextWindow", &decoded.ContextWindow); err != nil {
		return err
	}
	if err := decodeOptional(wireValue.MaxTokens, "catalog model maxTokens", &decoded.MaxTokens); err != nil {
		return err
	}
	*catalogEntry = decoded
	return nil
}

// UnmarshalJSON rejects null and unknown fields while preserving omission.
func (settings *Config) UnmarshalJSON(encoded []byte) error {
	if settings == nil {
		return errors.New("llm-deepseek: cannot decode config into nil target")
	}
	if bytes.Equal(bytes.TrimSpace(encoded), []byte("null")) {
		return errors.New("llm-deepseek: config must be an object")
	}
	var wireValue configWire
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wireValue); err != nil {
		return err
	}
	if err := requireJSONEOF(decoder); err != nil {
		return err
	}
	var decoded Config
	if err := decodeOptional(wireValue.APIKeyEnv, "apiKeyEnv", &decoded.APIKeyEnv); err != nil {
		return err
	}
	if err := decodeOptional(wireValue.BaseURL, "baseURL", &decoded.BaseURL); err != nil {
		return err
	}
	if err := decodeOptional(wireValue.Thinking, "thinking", &decoded.Thinking); err != nil {
		return err
	}
	if err := decodeOptional(wireValue.ReasoningEffort, "reasoningEffort", &decoded.ReasoningEffort); err != nil {
		return err
	}
	if err := decodeOptional(wireValue.MaxTokens, "maxTokens", &decoded.MaxTokens); err != nil {
		return err
	}
	if err := decodeOptional(wireValue.DefaultContextWindow, "defaultContextWindow", &decoded.DefaultContextWindow); err != nil {
		return err
	}
	if len(wireValue.Models) != 0 {
		if isNull(wireValue.Models) {
			return errors.New("llm-deepseek: models must be an array")
		}
		var models []CatalogModel
		if err := json.Unmarshal(wireValue.Models, &models); err != nil {
			return fmt.Errorf("llm-deepseek: models must be an array: %w", err)
		}
		decoded.Models = &models
	}
	if err := decodeOptional(wireValue.StreamIdleTimeoutMS, "streamIdleTimeoutMs", &decoded.StreamIdleTimeoutMS); err != nil {
		return err
	}
	if err := decodeOptional(wireValue.RetryPolicy, "retryPolicy", &decoded.RetryPolicy); err != nil {
		return err
	}
	*settings = decoded
	return nil
}

func decodeOptional[T any](rawValue json.RawMessage, fieldName string, destination **T) error {
	if len(rawValue) == 0 {
		return nil
	}
	if isNull(rawValue) {
		return fmt.Errorf("llm-deepseek: %s must not be null", fieldName)
	}
	var decoded T
	if err := json.Unmarshal(rawValue, &decoded); err != nil {
		return fmt.Errorf("llm-deepseek: invalid %s: %w", fieldName, err)
	}
	*destination = &decoded
	return nil
}

func isNull(rawValue json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(rawValue), []byte("null"))
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("llm-deepseek: unexpected trailing JSON")
		}
		return err
	}
	return nil
}

func cloneThinking(source *ThinkingMode) *ThinkingMode {
	if source == nil {
		return nil
	}
	value := *source
	return &value
}

func cloneReasoningEffort(source *ReasoningEffort) *ReasoningEffort {
	if source == nil {
		return nil
	}
	value := *source
	return &value
}

func cloneInt(source *int) *int {
	if source == nil {
		return nil
	}
	value := *source
	return &value
}

func intPointer(value int) *int { return &value }

func stringPointer(value string) *string { return &value }

func cloneString(source *string) *string {
	if source == nil {
		return nil
	}
	value := *source
	return &value
}
