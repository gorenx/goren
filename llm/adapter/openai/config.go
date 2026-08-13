package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gorenx/goren/llm"
	"github.com/openai/openai-go/v3/option"
)

// SystemRole controls how the adapter maps the provider-neutral system prompt.
type SystemRole string

const (
	// SystemRoleSystem emits a system message.
	SystemRoleSystem SystemRole = "system"
	// SystemRoleDeveloper emits a developer message.
	SystemRoleDeveloper SystemRole = "developer"
)

// MaxTokensField selects the compatible Chat Completions token-limit field.
type MaxTokensField string

const (
	// MaxTokensCompletion uses max_completion_tokens.
	MaxTokensCompletion MaxTokensField = "max_completion_tokens"
	// MaxTokensLegacy uses max_tokens.
	MaxTokensLegacy MaxTokensField = "max_tokens"
)

// ReasoningFormat selects a compatible provider reasoning request shape.
type ReasoningFormat string

const (
	// ReasoningFormatOpenAI uses reasoning_effort.
	ReasoningFormatOpenAI ReasoningFormat = "openai"
	// ReasoningFormatOpenRouter uses reasoning.effort.
	ReasoningFormatOpenRouter ReasoningFormat = "openrouter"
	// ReasoningFormatDeepSeek uses thinking.type and reasoning_effort.
	ReasoningFormatDeepSeek ReasoningFormat = "deepseek"
	// ReasoningFormatQwen uses enable_thinking.
	ReasoningFormatQwen ReasoningFormat = "qwen"
	// ReasoningFormatNone omits provider reasoning controls.
	ReasoningFormatNone ReasoningFormat = "none"
)

// Compatibility owns OpenAI-compatible wire differences. Its zero value uses
// official OpenAI behavior.
type Compatibility struct {
	SystemRole             SystemRole
	MaxTokensField         MaxTokensField
	ReasoningFormat        ReasoningFormat
	DisableStreamingUsage  bool
	DisableStrictTools     bool
	IncludeToolResultName  bool
	DisableToolChoice      bool
	SessionAffinityHeaders []string
	ThinkingBudgetField    string
	ToolErrorPrefix        string
}

func requestTransformOptions(ctx context.Context, targetModel llm.Model, invocationOptions llm.StreamOptions, requestBody any) ([]option.RequestOption, error) {
	if invocationOptions.TransformRequest == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("encode OpenAI request for transform: %w", err)
	}
	payload, err := invocationOptions.TransformRequest(ctx, requestInfo(targetModel, invocationOptions), llm.RequestPayload{
		Body: append(json.RawMessage(nil), encoded...),
		Set:  make(map[string]any),
	})
	if err != nil {
		return nil, err
	}
	if len(payload.Body) > 0 && !json.Valid(payload.Body) {
		return nil, errors.New("request transform returned invalid JSON")
	}
	if !bytes.Equal(payload.Body, encoded) {
		return nil, errors.New("request transform cannot replace inspection-only Body; use Set")
	}
	requestOptions := make([]option.RequestOption, 0, len(payload.Set))
	for path, value := range payload.Set {
		if strings.TrimSpace(path) == "" {
			return nil, errors.New("request transform field path cannot be empty")
		}
		requestOptions = append(requestOptions, option.WithJSONSet(path, value))
	}
	return requestOptions, nil
}

// AdapterOption configures one model-bound adapter without adding a factory.
type AdapterOption func(*adapterConfig)

type adapterConfig struct {
	compat        Compatibility
	systemRoleSet bool
}

// WithCompatibility applies explicit OpenAI-compatible protocol differences.
func WithCompatibility(compatibleBehavior Compatibility) AdapterOption {
	return func(configuration *adapterConfig) {
		configuration.compat = compatibleBehavior
		configuration.systemRoleSet = compatibleBehavior.SystemRole != ""
	}
}

func defaultAdapterConfig() adapterConfig {
	return adapterConfig{compat: Compatibility{
		SystemRole:             SystemRoleSystem,
		MaxTokensField:         MaxTokensCompletion,
		ReasoningFormat:        ReasoningFormatOpenAI,
		SessionAffinityHeaders: []string{"session_id"},
		ToolErrorPrefix:        "[tool_error] ",
	}}
}

func resolveAdapterConfig(adapterOptions []AdapterOption) (adapterConfig, error) {
	configuration := defaultAdapterConfig()
	for _, configure := range adapterOptions {
		if configure != nil {
			configure(&configuration)
		}
	}
	if configuration.compat.SystemRole == "" {
		configuration.compat.SystemRole = SystemRoleSystem
	}
	if configuration.compat.MaxTokensField == "" {
		configuration.compat.MaxTokensField = MaxTokensCompletion
	}
	if configuration.compat.ReasoningFormat == "" {
		configuration.compat.ReasoningFormat = ReasoningFormatOpenAI
	}
	if configuration.compat.ToolErrorPrefix == "" {
		configuration.compat.ToolErrorPrefix = "[tool_error] "
	}
	switch configuration.compat.SystemRole {
	case SystemRoleSystem, SystemRoleDeveloper:
	default:
		return adapterConfig{}, errors.New("unsupported OpenAI-compatible system role")
	}
	switch configuration.compat.MaxTokensField {
	case MaxTokensCompletion, MaxTokensLegacy:
	default:
		return adapterConfig{}, errors.New("unsupported OpenAI-compatible max tokens field")
	}
	switch configuration.compat.ReasoningFormat {
	case ReasoningFormatOpenAI, ReasoningFormatOpenRouter, ReasoningFormatDeepSeek, ReasoningFormatQwen, ReasoningFormatNone:
	default:
		return adapterConfig{}, errors.New("unsupported OpenAI-compatible reasoning format")
	}
	for _, headerName := range configuration.compat.SessionAffinityHeaders {
		if strings.TrimSpace(headerName) == "" {
			return adapterConfig{}, errors.New("session affinity header cannot be empty")
		}
	}
	configuration.compat.SessionAffinityHeaders = append([]string(nil), configuration.compat.SessionAffinityHeaders...)
	return configuration, nil
}

func runBeforeRequest(ctx context.Context, targetModel llm.Model, invocationOptions llm.StreamOptions) error {
	if invocationOptions.BeforeRequest == nil {
		return nil
	}
	return invocationOptions.BeforeRequest(ctx, requestInfo(targetModel, invocationOptions))
}

func requestInfo(targetModel llm.Model, invocationOptions llm.StreamOptions) llm.RequestInfo {
	metadata := make(map[string]string, len(invocationOptions.Metadata))
	for name, value := range invocationOptions.Metadata {
		metadata[name] = value
	}
	return llm.RequestInfo{
		API:       targetModel.API,
		Provider:  targetModel.Provider,
		Model:     targetModel.ID,
		RequestID: invocationOptions.RequestID,
		Metadata:  metadata,
	}
}

func runAfterResponse(ctx context.Context, invocationOptions llm.StreamOptions, response *http.Response) error {
	if invocationOptions.AfterResponse == nil || response == nil {
		return nil
	}
	headers := make(map[string][]string, len(response.Header))
	for name, values := range response.Header {
		headers[name] = append([]string(nil), values...)
	}
	requestID := response.Header.Get("x-request-id")
	if requestID == "" {
		requestID = invocationOptions.RequestID
	}
	return invocationOptions.AfterResponse(ctx, llm.ResponseInfo{
		RequestID:  requestID,
		StatusCode: response.StatusCode,
		Headers:    headers,
	})
}
