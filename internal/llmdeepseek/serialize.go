package llmdeepseek

import (
	"fmt"

	"github.com/gorenx/goren/llm"
)

const emptyToolOutput = "(no output)"

// SerializeMessages maps the Harness conversation vocabulary to the direct
// DeepSeek chat-completions wire vocabulary without dropping core images.
func SerializeMessages(conversation []llm.Message) ([]wireMessage, error) {
	wireMessages := make([]wireMessage, 0, len(conversation))
	for messageIndex, entry := range conversation {
		if entry == nil {
			return nil, fmt.Errorf("llm-deepseek: message %d is nil", messageIndex)
		}
		content := entry.ContentValue()
		if llm.ContentHasImage(content) {
			return nil, llm.MustLlmError(
				"The DeepSeek chat-completions adapter does not support image content.",
				"UNSUPPORTED_CONTENT",
			)
		}
		switch entry.ConversationRole() {
		case llm.RoleSystem:
			wireMessages = append(wireMessages, wireSystemMessage{Role: "system", Content: flattenText(content)})
		case llm.RoleAssistant:
			wireMessages = append(wireMessages, serializeAssistant(content))
		case llm.RoleUser:
			wireMessages = append(wireMessages, serializeUser(content)...)
		default:
			return nil, fmt.Errorf("llm-deepseek: message %d has unsupported role %q", messageIndex, entry.ConversationRole())
		}
	}
	return wireMessages, nil
}

func serializeAssistant(content []llm.ContentBlock) wireAssistantMessage {
	visibleText := flattenText(content)
	reasoningText := flattenContentType(content, "reasoning")
	toolCalls := make([]wireToolCall, 0)
	for _, entry := range content {
		call, found := toolCallValue(entry)
		if !found {
			continue
		}
		toolCalls = append(toolCalls, wireToolCall{
			ID: string(call.ID), Type: "function",
			Function: wireToolFunction{Name: call.Name, Arguments: call.Arguments},
		})
	}
	result := wireAssistantMessage{Role: "assistant", Content: stringPointer(visibleText)}
	if len(toolCalls) > 0 {
		result.ToolCalls = toolCalls
		if reasoningText != "" {
			result.ReasoningContent = stringPointer(reasoningText)
		}
	}
	return result
}

func serializeUser(content []llm.ContentBlock) []wireMessage {
	toolResults := make([]llm.ToolResultBlock, 0)
	for _, entry := range content {
		result, found := toolResultValue(entry)
		if found {
			toolResults = append(toolResults, result)
		}
	}
	visibleText := flattenText(content)
	wireMessages := make([]wireMessage, 0, len(toolResults)+1)
	if visibleText != "" || len(toolResults) == 0 {
		wireMessages = append(wireMessages, wireUserMessage{Role: "user", Content: visibleText})
	}
	for _, result := range toolResults {
		output := flattenText(result.Content)
		if output == "" {
			output = emptyToolOutput
		}
		wireMessages = append(wireMessages, wireToolMessage{
			Role: "tool", ToolCallID: string(result.ToolCallID), Content: output,
		})
	}
	return wireMessages
}

func flattenText(content []llm.ContentBlock) string {
	return flattenContentType(content, "text")
}

func flattenContentType(content []llm.ContentBlock, contentType string) string {
	flattened := ""
	for _, entry := range content {
		if entry == nil || entry.ContentType() != contentType {
			continue
		}
		if contentType == "text" {
			if readable, supported := entry.(llm.PlainTextContent); supported {
				if value, available := readable.PlainText(); available {
					flattened += value
				}
			}
			continue
		}
		switch block := entry.(type) {
		case llm.ReasoningBlock:
			flattened += block.Text
		case *llm.ReasoningBlock:
			if block != nil {
				flattened += block.Text
			}
		}
	}
	return flattened
}

func toolCallValue(entry llm.ContentBlock) (llm.ToolCallBlock, bool) {
	switch block := entry.(type) {
	case llm.ToolCallBlock:
		return block, true
	case *llm.ToolCallBlock:
		if block != nil {
			return *block, true
		}
	}
	return llm.ToolCallBlock{}, false
}

func toolResultValue(entry llm.ContentBlock) (llm.ToolResultBlock, bool) {
	switch block := entry.(type) {
	case llm.ToolResultBlock:
		return block, true
	case *llm.ToolResultBlock:
		if block != nil {
			return *block, true
		}
	}
	return llm.ToolResultBlock{}, false
}
