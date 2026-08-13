package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	// ErrUnsupportedModality means the target model cannot accept content in the Context.
	ErrUnsupportedModality = errors.New("LLM model does not support input modality")
	// ErrContextWindowExceeded means input plus reserved output exceeds the model limit.
	ErrContextWindowExceeded = errors.New("LLM context window exceeded")
)

// PrepareContext returns a target-compatible Context without modifying input.
// It removes failed turns and model-bound replay metadata, converts
// cross-model content, and repairs orphaned tool calls with explicit error
// results. Provider-specific wire identities remain the adapter's concern. The
// returned Context owns its top-level message and tool slices and tool schemas;
// unchanged message content is structurally shared and must be consumed
// synchronously by an adapter.
func PrepareContext(targetModel Model, input Context) (Context, error) {
	if input.SystemPrompt != "" && !ModelAccepts(targetModel, InputText) {
		return Context{}, fmt.Errorf("prepare system prompt: %w: text", ErrUnsupportedModality)
	}
	prepared := Context{
		SystemPrompt: input.SystemPrompt,
		Tools:        cloneTools(input.Tools),
		Messages:     make([]Message, 0, len(input.Messages)),
	}
	var pendingCalls []ToolCall
	var pendingTimestamp time.Time
	resolvedCalls := make(map[string]bool)

	flushPending := func() {
		for _, pendingCall := range pendingCalls {
			if resolvedCalls[pendingCall.ID] {
				continue
			}
			prepared.Messages = append(prepared.Messages, ToolResultMessage{
				ToolCallID: pendingCall.ID,
				ToolName:   pendingCall.Name,
				Content:    []ToolResultContent{TextContent{Text: "No result provided"}},
				IsError:    true,
				Timestamp:  pendingTimestamp,
			})
		}
		pendingCalls = nil
		pendingTimestamp = time.Time{}
		clear(resolvedCalls)
	}

	for messageIndex, conversationEntry := range input.Messages {
		switch typedMessage := conversationEntry.(type) {
		case UserMessage:
			flushPending()
			clonedMessage, err := prepareUserMessage(targetModel, typedMessage)
			if err != nil {
				return Context{}, fmt.Errorf("prepare user message %d: %w", messageIndex, err)
			}
			prepared.Messages = append(prepared.Messages, clonedMessage)
		case AssistantMessage:
			flushPending()
			if typedMessage.StopReason == StopReasonError || typedMessage.StopReason == StopReasonAborted {
				continue
			}
			clonedMessage, calls, err := prepareAssistantMessage(targetModel, typedMessage)
			if err != nil {
				return Context{}, fmt.Errorf("prepare assistant message %d: %w", messageIndex, err)
			}
			if len(clonedMessage.Content) == 0 {
				continue
			}
			prepared.Messages = append(prepared.Messages, clonedMessage)
			pendingCalls = calls
			pendingTimestamp = clonedMessage.Timestamp
		case ToolResultMessage:
			clonedMessage, err := prepareToolResultMessage(targetModel, typedMessage)
			if err != nil {
				return Context{}, fmt.Errorf("prepare tool result message %d: %w", messageIndex, err)
			}
			if !pendingContains(pendingCalls, clonedMessage.ToolCallID) {
				continue
			}
			resolvedCalls[clonedMessage.ToolCallID] = true
			prepared.Messages = append(prepared.Messages, clonedMessage)
		default:
			return Context{}, fmt.Errorf("prepare message %d: unsupported type %T", messageIndex, conversationEntry)
		}
	}
	flushPending()
	return prepared, nil
}

func prepareUserMessage(targetModel Model, input UserMessage) (UserMessage, error) {
	for _, contentBlock := range input.Content {
		switch contentBlock.(type) {
		case TextContent:
			if !ModelAccepts(targetModel, InputText) {
				return UserMessage{}, fmt.Errorf("%w: text", ErrUnsupportedModality)
			}
		case ImageContent:
			if !ModelAccepts(targetModel, InputImage) {
				return UserMessage{}, fmt.Errorf("%w: image", ErrUnsupportedModality)
			}
		default:
			return UserMessage{}, fmt.Errorf("unsupported content %T", contentBlock)
		}
	}
	return input, nil
}

func prepareAssistantMessage(targetModel Model, input AssistantMessage) (AssistantMessage, []ToolCall, error) {
	sameModel := input.API == targetModel.API && input.Provider == targetModel.Provider && input.Model == targetModel.ID
	if sameModel {
		var calls []ToolCall
		for _, contentBlock := range input.Content {
			switch typedContent := contentBlock.(type) {
			case AssistantTextContent:
				if !ModelAccepts(targetModel, InputText) {
					return AssistantMessage{}, nil, fmt.Errorf("%w: text", ErrUnsupportedModality)
				}
			case ThinkingContent:
			case ToolCall:
				calls = append(calls, typedContent)
			default:
				return AssistantMessage{}, nil, fmt.Errorf("unsupported content %T", contentBlock)
			}
		}
		return input, calls, nil
	}

	prepared := input
	prepared.Content = make([]AssistantContent, 0, len(input.Content))
	var calls []ToolCall
	for _, contentBlock := range input.Content {
		switch typedContent := contentBlock.(type) {
		case AssistantTextContent:
			if !ModelAccepts(targetModel, InputText) {
				return AssistantMessage{}, nil, fmt.Errorf("%w: text", ErrUnsupportedModality)
			}
			clonedContent := typedContent
			clonedContent.Metadata = nil
			prepared.Content = append(prepared.Content, clonedContent)
		case ThinkingContent:
			if typedContent.Redacted || strings.TrimSpace(typedContent.Thinking) == "" {
				continue
			}
			if !ModelAccepts(targetModel, InputText) {
				return AssistantMessage{}, nil, fmt.Errorf("%w: text", ErrUnsupportedModality)
			}
			prepared.Content = append(prepared.Content, AssistantTextContent{Text: typedContent.Thinking, Phase: AssistantTextPhaseUnspecified})
		case ToolCall:
			clonedContent := typedContent
			clonedContent.Metadata = nil
			prepared.Content = append(prepared.Content, clonedContent)
			calls = append(calls, clonedContent)
		default:
			return AssistantMessage{}, nil, fmt.Errorf("unsupported content %T", contentBlock)
		}
	}
	return prepared, calls, nil
}

func prepareToolResultMessage(targetModel Model, input ToolResultMessage) (ToolResultMessage, error) {
	for _, contentBlock := range input.Content {
		switch contentBlock.(type) {
		case TextContent:
			if !ModelAccepts(targetModel, InputText) {
				return ToolResultMessage{}, fmt.Errorf("%w: text", ErrUnsupportedModality)
			}
		case ImageContent:
			if !ModelAccepts(targetModel, InputImage) {
				return ToolResultMessage{}, fmt.Errorf("%w: image", ErrUnsupportedModality)
			}
		default:
			return ToolResultMessage{}, fmt.Errorf("unsupported content %T", contentBlock)
		}
	}
	return input, nil
}

func pendingContains(pendingCalls []ToolCall, toolCallID string) bool {
	for _, pendingCall := range pendingCalls {
		if pendingCall.ID == toolCallID {
			return true
		}
	}
	return false
}

// ModelAccepts reports whether targetModel accepts modality.
func ModelAccepts(targetModel Model, modality InputModality) bool {
	for _, supportedModality := range targetModel.Input {
		if supportedModality == modality {
			return true
		}
	}
	return false
}

func cloneTools(toolDefinitions []Tool) []Tool {
	if toolDefinitions == nil {
		return nil
	}
	cloned := make([]Tool, len(toolDefinitions))
	for index, toolDefinition := range toolDefinitions {
		cloned[index] = toolDefinition
		cloned[index].Parameters = cloneRawMessage(toolDefinition.Parameters)
	}
	return cloned
}

func attachToolValidators(toolDefinitions []Tool, compiledSchemas map[string]compiledToolSchema) {
	for index := range toolDefinitions {
		compiledSchema, ok := compiledSchemas[toolDefinitions[index].Name]
		if !ok || compiledSchema.parameters != string(toolDefinitions[index].Parameters) {
			continue
		}
		toolDefinitions[index].validator = compiledSchema.validator
		toolDefinitions[index].validated = compiledSchema.parameters
	}
}

func cloneRawMessage(raw json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), raw...)
}
