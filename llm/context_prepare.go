package llm

import (
	"crypto/sha256"
	"encoding/hex"
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

// PrepareContext returns an isolated, target-compatible copy of input. It
// removes failed turns and model-bound replay metadata, normalizes cross-model
// tool IDs, and repairs orphaned tool calls with explicit error results.
func PrepareContext(targetModel Model, input Context) (Context, error) {
	if input.SystemPrompt != "" && !ModelAccepts(targetModel, InputText) {
		return Context{}, fmt.Errorf("prepare system prompt: %w: text", ErrUnsupportedModality)
	}
	prepared := Context{
		SystemPrompt: input.SystemPrompt,
		Tools:        cloneTools(input.Tools),
		Messages:     make([]Message, 0, len(input.Messages)),
	}
	toolIDMap := make(map[string]string)
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
			clonedMessage, calls, mappings, err := prepareAssistantMessage(targetModel, typedMessage)
			if err != nil {
				return Context{}, fmt.Errorf("prepare assistant message %d: %w", messageIndex, err)
			}
			for originalID, normalizedID := range mappings {
				toolIDMap[originalID] = normalizedID
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
			if normalizedID := toolIDMap[clonedMessage.ToolCallID]; normalizedID != "" {
				clonedMessage.ToolCallID = normalizedID
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
	prepared := UserMessage{Timestamp: input.Timestamp, Content: make([]UserContent, 0, len(input.Content))}
	for _, contentBlock := range input.Content {
		switch typedContent := contentBlock.(type) {
		case TextContent:
			if !ModelAccepts(targetModel, InputText) {
				return UserMessage{}, fmt.Errorf("%w: text", ErrUnsupportedModality)
			}
			prepared.Content = append(prepared.Content, typedContent)
		case ImageContent:
			if !ModelAccepts(targetModel, InputImage) {
				return UserMessage{}, fmt.Errorf("%w: image", ErrUnsupportedModality)
			}
			prepared.Content = append(prepared.Content, typedContent)
		default:
			return UserMessage{}, fmt.Errorf("unsupported content %T", contentBlock)
		}
	}
	return prepared, nil
}

func prepareAssistantMessage(targetModel Model, input AssistantMessage) (AssistantMessage, []ToolCall, map[string]string, error) {
	sameModel := input.API == targetModel.API && input.Provider == targetModel.Provider && input.Model == targetModel.ID
	prepared := input
	prepared.Content = make([]AssistantContent, 0, len(input.Content))
	var calls []ToolCall
	mappings := make(map[string]string)
	for _, contentBlock := range input.Content {
		switch typedContent := contentBlock.(type) {
		case AssistantTextContent:
			if !ModelAccepts(targetModel, InputText) {
				return AssistantMessage{}, nil, nil, fmt.Errorf("%w: text", ErrUnsupportedModality)
			}
			clonedContent := typedContent
			clonedContent.Metadata = cloneCompatibleMetadata(typedContent.Metadata, targetModel, sameModel)
			prepared.Content = append(prepared.Content, clonedContent)
		case ThinkingContent:
			if sameModel {
				clonedContent := typedContent
				clonedContent.Metadata = cloneCompatibleMetadata(typedContent.Metadata, targetModel, true)
				prepared.Content = append(prepared.Content, clonedContent)
				continue
			}
			if typedContent.Redacted || strings.TrimSpace(typedContent.Thinking) == "" {
				continue
			}
			if !ModelAccepts(targetModel, InputText) {
				return AssistantMessage{}, nil, nil, fmt.Errorf("%w: text", ErrUnsupportedModality)
			}
			prepared.Content = append(prepared.Content, AssistantTextContent{Text: typedContent.Thinking, Phase: AssistantTextPhaseUnspecified})
		case ToolCall:
			clonedContent := typedContent
			clonedContent.Arguments = cloneRawMessage(typedContent.Arguments)
			if !sameModel {
				clonedContent.Metadata = nil
				clonedContent.ID = normalizeToolCallID(typedContent.ID)
				mappings[typedContent.ID] = clonedContent.ID
			} else {
				clonedContent.Metadata = cloneCompatibleMetadata(typedContent.Metadata, targetModel, true)
			}
			prepared.Content = append(prepared.Content, clonedContent)
			calls = append(calls, clonedContent)
		default:
			return AssistantMessage{}, nil, nil, fmt.Errorf("unsupported content %T", contentBlock)
		}
	}
	return prepared, calls, mappings, nil
}

func prepareToolResultMessage(targetModel Model, input ToolResultMessage) (ToolResultMessage, error) {
	prepared := input
	prepared.Content = make([]ToolResultContent, 0, len(input.Content))
	for _, contentBlock := range input.Content {
		switch typedContent := contentBlock.(type) {
		case TextContent:
			if !ModelAccepts(targetModel, InputText) {
				return ToolResultMessage{}, fmt.Errorf("%w: text", ErrUnsupportedModality)
			}
			prepared.Content = append(prepared.Content, typedContent)
		case ImageContent:
			if !ModelAccepts(targetModel, InputImage) {
				return ToolResultMessage{}, fmt.Errorf("%w: image", ErrUnsupportedModality)
			}
			prepared.Content = append(prepared.Content, typedContent)
		default:
			return ToolResultMessage{}, fmt.Errorf("unsupported content %T", contentBlock)
		}
	}
	return prepared, nil
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

func cloneCompatibleMetadata(metadata *ReplayMetadata, targetModel Model, sameModel bool) *ReplayMetadata {
	if metadata == nil || !sameModel || metadata.API != targetModel.API || metadata.Provider != targetModel.Provider || metadata.Model != targetModel.ID {
		return nil
	}
	cloned := *metadata
	cloned.Data = cloneRawMessage(metadata.Data)
	return &cloned
}

func normalizeToolCallID(toolCallID string) string {
	valid := toolCallID != "" && len(toolCallID) <= 64
	for _, character := range toolCallID {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '_' || character == '-' {
			continue
		}
		valid = false
		break
	}
	if valid {
		return toolCallID
	}
	digest := sha256.Sum256([]byte(toolCallID))
	return "call_" + hex.EncodeToString(digest[:])[:40]
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

func cloneRawMessage(raw json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), raw...)
}
