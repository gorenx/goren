package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
)

var (
	// ErrAdapterNotRegistered means no adapter constructor owns the requested wire protocol.
	ErrAdapterNotRegistered = errors.New("LLM API adapter not registered")
	// ErrAPIMismatch means an adapter was invoked with a model for another protocol.
	ErrAPIMismatch = errors.New("model API does not match adapter API")
	// ErrInvalidStream means an adapter producer returned without a terminal event.
	ErrInvalidStream = errors.New("LLM stream ended without a terminal event")
)

// ValidateModel checks model identity, routing, endpoint, and token limits.
func ValidateModel(targetModel Model) error {
	if targetModel.ID == "" {
		return errors.New("model ID is required")
	}
	if targetModel.API == "" {
		return errors.New("model API is required")
	}
	if targetModel.Provider == "" {
		return errors.New("model provider is required")
	}
	if targetModel.BaseURL == "" {
		return errors.New("model base URL is required")
	}
	baseURL, err := url.Parse(targetModel.BaseURL)
	if err != nil || baseURL.Host == "" || (baseURL.Scheme != "http" && baseURL.Scheme != "https") {
		return fmt.Errorf("invalid model base URL %q", targetModel.BaseURL)
	}
	if targetModel.ContextWindow < 0 || targetModel.MaxOutputTokens < 0 {
		return errors.New("model token limits cannot be negative")
	}
	return nil
}

// ValidateContext checks message and tool invariants before adapter execution.
func ValidateContext(input Context) error {
	for index, conversationEntry := range input.Messages {
		if conversationEntry == nil {
			return fmt.Errorf("message %d is nil", index)
		}
		switch value := conversationEntry.(type) {
		case UserMessage:
			if len(value.Content) == 0 {
				return fmt.Errorf("user message %d has no content", index)
			}
		case AssistantMessage:
			if len(value.Content) == 0 && value.StopReason != StopReasonError && value.StopReason != StopReasonAborted {
				return fmt.Errorf("assistant message %d has no content", index)
			}
		case ToolResultMessage:
			if value.ToolCallID == "" || value.ToolName == "" {
				return fmt.Errorf("tool result message %d requires call ID and name", index)
			}
			if len(value.Content) == 0 {
				return fmt.Errorf("tool result message %d has no content", index)
			}
		default:
			return fmt.Errorf("message %d has unsupported type %T", index, conversationEntry)
		}
	}

	for index, toolDefinition := range input.Tools {
		if toolDefinition.Name == "" {
			return fmt.Errorf("tool %d has no name", index)
		}
		if len(toolDefinition.Parameters) == 0 || !json.Valid(toolDefinition.Parameters) {
			return fmt.Errorf("tool %q has invalid parameter schema", toolDefinition.Name)
		}
	}
	return nil
}

// ValidateOptions checks invocation controls that are shared by adapters.
func ValidateOptions(invocationOptions StreamOptions) error {
	if invocationOptions.Temperature != nil && (*invocationOptions.Temperature < 0 || *invocationOptions.Temperature > 2) {
		return errors.New("temperature must be between 0 and 2")
	}
	if invocationOptions.MaxOutputTokens < 0 {
		return errors.New("max output tokens cannot be negative")
	}
	if responseSchema := invocationOptions.ResponseFormat; responseSchema != nil {
		if responseSchema.Name == "" {
			return errors.New("response format name is required")
		}
		if len(responseSchema.Schema) == 0 || !json.Valid(responseSchema.Schema) {
			return errors.New("response format schema is invalid")
		}
	}
	return nil
}
