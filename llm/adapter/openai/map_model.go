package openai

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/gorenx/goren/llm"
	officialopenai "github.com/openai/openai-go/v3"
)

// cloneModel gives an adapter its own snapshot when its constructor is used
// directly or by a custom composition root.
func cloneModel(targetModel llm.Model) llm.Model {
	cloned := targetModel
	cloned.Input = append([]llm.InputModality(nil), targetModel.Input...)
	cloned.ReasoningLevels = append([]llm.ReasoningLevel(nil), targetModel.ReasoningLevels...)
	if targetModel.ReasoningMap != nil {
		cloned.ReasoningMap = make(map[llm.ReasoningLevel]string, len(targetModel.ReasoningMap))
		for level, mapped := range targetModel.ReasoningMap {
			cloned.ReasoningMap[level] = mapped
		}
	}
	if targetModel.ReasoningBudget != nil {
		cloned.ReasoningBudget = make(map[llm.ReasoningLevel]int, len(targetModel.ReasoningBudget))
		for level, budget := range targetModel.ReasoningBudget {
			cloned.ReasoningBudget[level] = budget
		}
	}
	if targetModel.Headers != nil {
		cloned.Headers = make(map[string]string, len(targetModel.Headers))
		for name, value := range targetModel.Headers {
			cloned.Headers[name] = value
		}
	}
	if targetModel.ServiceTierCost != nil {
		cloned.ServiceTierCost = make(map[string]float64, len(targetModel.ServiceTierCost))
		for tier, multiplier := range targetModel.ServiceTierCost {
			cloned.ServiceTierCost[tier] = multiplier
		}
	}
	return cloned
}

func mapMessages(targetModel llm.Model, input llm.Context, compatibleBehavior Compatibility) ([]officialopenai.ChatCompletionMessageParamUnion, error) {
	messages := make([]officialopenai.ChatCompletionMessageParamUnion, 0, len(input.Messages)+1)
	toolCallIDs := make(map[string]string)
	if input.SystemPrompt != "" {
		if compatibleBehavior.SystemRole == SystemRoleDeveloper {
			messages = append(messages, officialopenai.DeveloperMessage(input.SystemPrompt))
		} else {
			messages = append(messages, officialopenai.SystemMessage(input.SystemPrompt))
		}
	}
	for index := 0; index < len(input.Messages); index++ {
		conversationEntry := input.Messages[index]
		switch value := conversationEntry.(type) {
		case llm.UserMessage:
			mapped, err := mapUserMessage(value)
			if err != nil {
				return nil, fmt.Errorf("map user message %d: %w", index, err)
			}
			messages = append(messages, mapped)
		case llm.AssistantMessage:
			mapped, mappedIDs, err := mapAssistantMessage(targetModel, value)
			if err != nil {
				return nil, fmt.Errorf("map assistant message %d: %w", index, err)
			}
			messages = append(messages, mapped)
			for sourceID, wireID := range mappedIDs {
				toolCallIDs[sourceID] = wireID
			}
		case llm.ToolResultMessage:
			var imageParts []officialopenai.ChatCompletionContentPartUnionParam
			for ; index < len(input.Messages); index++ {
				toolResult, ok := input.Messages[index].(llm.ToolResultMessage)
				if !ok {
					break
				}
				resultText, images, err := mapToolResultContent(toolResult.Content)
				if err != nil {
					return nil, fmt.Errorf("map tool result message %d: %w", index, err)
				}
				if resultText == "" && len(images) > 0 {
					resultText = "(see attached image)"
				}
				if toolResult.IsError {
					resultText = compatibleBehavior.ToolErrorPrefix + resultText
				}
				mappedToolResult := toolResult
				if wireID := toolCallIDs[toolResult.ToolCallID]; wireID != "" {
					mappedToolResult.ToolCallID = wireID
				}
				mappedResult, err := mapToolMessage(resultText, mappedToolResult, compatibleBehavior)
				if err != nil {
					return nil, fmt.Errorf("map tool result message %d: %w", index, err)
				}
				messages = append(messages, mappedResult)
				for _, image := range images {
					imageParts = append(imageParts, officialopenai.ImageContentPart(
						officialopenai.ChatCompletionContentPartImageImageURLParam{URL: "data:" + image.MIMEType + ";base64," + image.Data},
					))
				}
			}
			index--
			if len(imageParts) > 0 {
				parts := make([]officialopenai.ChatCompletionContentPartUnionParam, 0, len(imageParts)+1)
				parts = append(parts, officialopenai.TextContentPart("Attached image(s) from tool result:"))
				parts = append(parts, imageParts...)
				messages = append(messages, officialopenai.UserMessage(parts))
			}
		default:
			return nil, fmt.Errorf("message %d has unsupported type %T", index, conversationEntry)
		}
	}
	return messages, nil
}

func mapUserMessage(userInput llm.UserMessage) (officialopenai.ChatCompletionMessageParamUnion, error) {
	if len(userInput.Content) == 1 {
		if textBlock, ok := userInput.Content[0].(llm.TextContent); ok {
			return officialopenai.UserMessage(textBlock.Text), nil
		}
	}

	parts := make([]officialopenai.ChatCompletionContentPartUnionParam, 0, len(userInput.Content))
	for _, block := range userInput.Content {
		switch value := block.(type) {
		case llm.TextContent:
			parts = append(parts, officialopenai.TextContentPart(value.Text))
		case llm.ImageContent:
			if value.MIMEType == "" || value.Data == "" {
				return officialopenai.ChatCompletionMessageParamUnion{}, errors.New("image content requires MIME type and base64 data")
			}
			parts = append(parts, officialopenai.ImageContentPart(
				officialopenai.ChatCompletionContentPartImageImageURLParam{
					URL: "data:" + value.MIMEType + ";base64," + value.Data,
				},
			))
		default:
			return officialopenai.ChatCompletionMessageParamUnion{}, fmt.Errorf("unsupported user content %T", block)
		}
	}
	return officialopenai.UserMessage(parts), nil
}

func mapAssistantMessage(targetModel llm.Model, assistantInput llm.AssistantMessage) (officialopenai.ChatCompletionMessageParamUnion, map[string]string, error) {
	var visibleText strings.Builder
	mapped := officialopenai.ChatCompletionAssistantMessageParam{}
	mappedIDs := make(map[string]string)
	for _, block := range assistantInput.Content {
		switch value := block.(type) {
		case llm.AssistantTextContent:
			visibleText.WriteString(value.Text)
		case llm.ThinkingContent:
			// Chat Completions has no portable replay field for reasoning.
			continue
		case llm.ToolCall:
			arguments := value.Arguments
			if len(arguments) == 0 {
				arguments = json.RawMessage(`{}`)
			}
			if !json.Valid(arguments) {
				return officialopenai.ChatCompletionMessageParamUnion{}, nil, fmt.Errorf("tool call %q has invalid arguments", value.Name)
			}
			wireID := normalizeChatToolCallID(value.ID, targetModel)
			mappedIDs[value.ID] = wireID
			mapped.ToolCalls = append(mapped.ToolCalls, officialopenai.ChatCompletionMessageToolCallUnionParam{
				OfFunction: &officialopenai.ChatCompletionMessageFunctionToolCallParam{
					ID: wireID,
					Function: officialopenai.ChatCompletionMessageFunctionToolCallFunctionParam{
						Name:      value.Name,
						Arguments: string(arguments),
					},
				},
			})
		default:
			return officialopenai.ChatCompletionMessageParamUnion{}, nil, fmt.Errorf("unsupported assistant content %T", block)
		}
	}
	if visibleText.Len() > 0 {
		mapped.Content.OfString = officialopenai.String(visibleText.String())
	}
	return officialopenai.ChatCompletionMessageParamUnion{OfAssistant: &mapped}, mappedIDs, nil
}

func normalizeChatToolCallID(toolCallID string, targetModel llm.Model) string {
	callID, _, _ := strings.Cut(toolCallID, "|")
	maximumLength := 64
	if targetModel.Provider == "openai" {
		maximumLength = 40
	}
	return normalizeOpenAIToolCallID(callID, maximumLength)
}

func normalizeOpenAIToolCallID(toolCallID string, maximumLength int) string {
	if validOpenAIWireID(toolCallID, maximumLength) {
		return toolCallID
	}
	return hashedOpenAIWireID("call_", toolCallID, maximumLength)
}

func normalizeResponsesItemID(itemID string) string {
	if strings.HasPrefix(itemID, "fc_") && validOpenAIWireID(itemID, 64) {
		return itemID
	}
	return hashedOpenAIWireID("fc_", itemID, 64)
}

func validOpenAIWireID(identifier string, maximumLength int) bool {
	if identifier == "" || len(identifier) > maximumLength {
		return false
	}
	for _, character := range identifier {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func hashedOpenAIWireID(prefix string, source string, maximumLength int) string {
	if maximumLength <= 0 {
		return ""
	}
	if maximumLength <= len(prefix) {
		return prefix[:maximumLength]
	}
	digest := sha256.Sum256([]byte(source))
	encoded := hex.EncodeToString(digest[:])
	return prefix + encoded[:min(len(encoded), maximumLength-len(prefix))]
}

func mapToolResultContent(resultBlocks []llm.ToolResultContent) (string, []llm.ImageContent, error) {
	var resultText strings.Builder
	var images []llm.ImageContent
	for _, block := range resultBlocks {
		switch value := block.(type) {
		case llm.TextContent:
			resultText.WriteString(value.Text)
		case llm.ImageContent:
			if value.MIMEType == "" || value.Data == "" {
				return "", nil, errors.New("image content requires MIME type and base64 data")
			}
			images = append(images, value)
		default:
			return "", nil, fmt.Errorf("unsupported tool result content %T", block)
		}
	}
	return resultText.String(), images, nil
}

func mapTools(toolDefinitions []llm.Tool, compatibleBehavior Compatibility) ([]officialopenai.ChatCompletionToolUnionParam, error) {
	mapped := make([]officialopenai.ChatCompletionToolUnionParam, 0, len(toolDefinitions))
	for _, toolDefinition := range toolDefinitions {
		var parameterSchema officialopenai.FunctionParameters
		if err := json.Unmarshal(toolDefinition.Parameters, &parameterSchema); err != nil {
			return nil, fmt.Errorf("map tool %q parameters: %w", toolDefinition.Name, err)
		}
		definition := officialopenai.FunctionDefinitionParam{
			Name:       toolDefinition.Name,
			Parameters: parameterSchema,
		}
		if toolDefinition.Description != "" {
			definition.Description = officialopenai.String(toolDefinition.Description)
		}
		if toolDefinition.Strict && !compatibleBehavior.DisableStrictTools {
			definition.Strict = officialopenai.Bool(true)
		}
		mapped = append(mapped, officialopenai.ChatCompletionFunctionTool(definition))
	}
	return mapped, nil
}

func mapToolMessage(resultText string, toolResult llm.ToolResultMessage, compatibleBehavior Compatibility) (officialopenai.ChatCompletionMessageParamUnion, error) {
	mapped := officialopenai.ToolMessage(resultText, toolResult.ToolCallID)
	if !compatibleBehavior.IncludeToolResultName || toolResult.ToolName == "" {
		return mapped, nil
	}
	if mapped.OfTool == nil {
		return officialopenai.ChatCompletionMessageParamUnion{}, errors.New("OpenAI SDK did not create a tool message")
	}
	mapped.OfTool.SetExtraFields(map[string]any{"name": toolResult.ToolName})
	return mapped, nil
}

func mapChatToolChoice(toolSelection llm.ToolChoice) (officialopenai.ChatCompletionToolChoiceOptionUnionParam, error) {
	switch toolSelection.Mode {
	case llm.ToolChoiceAuto, llm.ToolChoiceNone, llm.ToolChoiceRequired:
		var mapped officialopenai.ChatCompletionToolChoiceOptionUnionParam
		if err := json.Unmarshal([]byte(`"`+string(toolSelection.Mode)+`"`), &mapped); err != nil {
			return officialopenai.ChatCompletionToolChoiceOptionUnionParam{}, fmt.Errorf("map tool choice: %w", err)
		}
		return mapped, nil
	case llm.ToolChoiceFunction:
		return officialopenai.ToolChoiceOptionFunctionToolChoice(
			officialopenai.ChatCompletionNamedToolChoiceFunctionParam{Name: toolSelection.Name},
		), nil
	default:
		return officialopenai.ChatCompletionToolChoiceOptionUnionParam{}, fmt.Errorf("unsupported tool choice %q", toolSelection.Mode)
	}
}

func mapResponseFormat(schemaFormat llm.JSONSchemaFormat) (officialopenai.ResponseFormatJSONSchemaParam, error) {
	var schema any
	if err := json.Unmarshal(schemaFormat.Schema, &schema); err != nil {
		return officialopenai.ResponseFormatJSONSchemaParam{}, fmt.Errorf("map response format %q schema: %w", schemaFormat.Name, err)
	}
	mapped := officialopenai.ResponseFormatJSONSchemaParam{
		JSONSchema: officialopenai.ResponseFormatJSONSchemaJSONSchemaParam{
			Name:   schemaFormat.Name,
			Schema: schema,
			Strict: officialopenai.Bool(schemaFormat.Strict),
		},
	}
	if schemaFormat.Description != "" {
		mapped.JSONSchema.Description = officialopenai.String(schemaFormat.Description)
	}
	return mapped, nil
}

func mapStopReason(providerReason string) (llm.StopReason, string) {
	switch providerReason {
	case "stop", "end":
		return llm.StopReasonStop, ""
	case "length":
		return llm.StopReasonLength, ""
	case "tool_calls", "function_call":
		return llm.StopReasonToolUse, ""
	default:
		return llm.StopReasonError, "provider finish reason: " + providerReason
	}
}
