package openai

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/gorenx/goren/llm"
	officialopenai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
)

func mapResponsesMessages(targetModel llm.Model, input llm.Context) (responses.ResponseInputParam, error) {
	items := make(responses.ResponseInputParam, 0, len(input.Messages)+1)
	if input.SystemPrompt != "" {
		role := responses.EasyInputMessageRoleSystem
		if targetModel.Reasoning {
			role = responses.EasyInputMessageRoleDeveloper
		}
		items = append(items, responses.ResponseInputItemParamOfMessage(input.SystemPrompt, role))
	}
	for messageIndex, conversationEntry := range input.Messages {
		switch value := conversationEntry.(type) {
		case llm.UserMessage:
			mapped, err := mapResponsesUserMessage(value)
			if err != nil {
				return nil, fmt.Errorf("map user message %d: %w", messageIndex, err)
			}
			items = append(items, mapped)
		case llm.AssistantMessage:
			mapped, err := mapResponsesAssistantMessage(value, messageIndex)
			if err != nil {
				return nil, fmt.Errorf("map assistant message %d: %w", messageIndex, err)
			}
			items = append(items, mapped...)
		case llm.ToolResultMessage:
			mapped, err := mapResponsesToolResult(targetModel, value)
			if err != nil {
				return nil, fmt.Errorf("map tool result message %d: %w", messageIndex, err)
			}
			items = append(items, mapped)
		default:
			return nil, fmt.Errorf("message %d has unsupported type %T", messageIndex, conversationEntry)
		}
	}
	return items, nil
}

func mapResponsesUserMessage(userInput llm.UserMessage) (responses.ResponseInputItemUnionParam, error) {
	content := make(responses.ResponseInputMessageContentListParam, 0, len(userInput.Content))
	for _, block := range userInput.Content {
		switch value := block.(type) {
		case llm.TextContent:
			content = append(content, responses.ResponseInputContentParamOfInputText(value.Text))
		case llm.ImageContent:
			if value.MIMEType == "" || value.Data == "" {
				return responses.ResponseInputItemUnionParam{}, errors.New("image content requires MIME type and base64 data")
			}
			image := responses.ResponseInputContentParamOfInputImage(responses.ResponseInputImageDetailAuto)
			image.OfInputImage.ImageURL = officialopenai.String("data:" + value.MIMEType + ";base64," + value.Data)
			content = append(content, image)
		default:
			return responses.ResponseInputItemUnionParam{}, fmt.Errorf("unsupported user content %T", block)
		}
	}
	return responses.ResponseInputItemParamOfMessage(content, responses.EasyInputMessageRoleUser), nil
}

func mapResponsesAssistantMessage(
	assistantInput llm.AssistantMessage,
	messageIndex int,
) ([]responses.ResponseInputItemUnionParam, error) {
	items := make([]responses.ResponseInputItemUnionParam, 0, len(assistantInput.Content))
	textIndex := 0
	for _, block := range assistantInput.Content {
		switch value := block.(type) {
		case llm.ThinkingContent:
			if value.Signature == "" || assistantInput.API != llm.APIOpenAIResponses {
				continue
			}
			var reasoning responses.ResponseReasoningItemParam
			if err := json.Unmarshal([]byte(value.Signature), &reasoning); err != nil {
				return nil, fmt.Errorf("decode reasoning replay metadata: %w", err)
			}
			items = append(items, responses.ResponseInputItemUnionParam{OfReasoning: &reasoning})
		case llm.TextContent:
			messageID := fmt.Sprintf("msg_goren_%d_%d", messageIndex, textIndex)
			textIndex++
			outputText := responses.ResponseOutputTextParam{
				Text:        value.Text,
				Annotations: []responses.ResponseOutputTextAnnotationUnionParam{},
			}
			message := responses.ResponseInputItemParamOfOutputMessage(
				[]responses.ResponseOutputMessageContentUnionParam{{OfOutputText: &outputText}},
				messageID,
				responses.ResponseOutputMessageStatusCompleted,
			)
			items = append(items, message)
		case llm.ToolCall:
			arguments := value.Arguments
			if len(arguments) == 0 {
				arguments = json.RawMessage(`{}`)
			}
			if !json.Valid(arguments) {
				return nil, fmt.Errorf("tool call %q has invalid arguments", value.Name)
			}
			callID, itemID := splitResponsesToolCallID(value.ID)
			functionCall := responses.ResponseInputItemParamOfFunctionCall(string(arguments), callID, value.Name)
			if itemID != "" {
				functionCall.OfFunctionCall.ID = officialopenai.String(itemID)
			}
			items = append(items, functionCall)
		default:
			return nil, fmt.Errorf("unsupported assistant content %T", block)
		}
	}
	return items, nil
}

func mapResponsesToolResult(
	targetModel llm.Model,
	toolResult llm.ToolResultMessage,
) (responses.ResponseInputItemUnionParam, error) {
	callID, _ := splitResponsesToolCallID(toolResult.ToolCallID)
	var textResult strings.Builder
	hasImages := false
	for _, block := range toolResult.Content {
		switch value := block.(type) {
		case llm.TextContent:
			textResult.WriteString(value.Text)
		case llm.ImageContent:
			if value.MIMEType == "" || value.Data == "" {
				return responses.ResponseInputItemUnionParam{}, errors.New("image content requires MIME type and base64 data")
			}
			hasImages = true
		default:
			return responses.ResponseInputItemUnionParam{}, fmt.Errorf("unsupported tool result content %T", block)
		}
	}

	if hasImages && modelAccepts(targetModel, llm.InputImage) {
		content := make(responses.ResponseFunctionCallOutputItemListParam, 0, len(toolResult.Content))
		if textResult.Len() > 0 {
			content = append(content, responses.ResponseFunctionCallOutputItemParamOfInputText(textResult.String()))
		}
		for _, block := range toolResult.Content {
			image, ok := block.(llm.ImageContent)
			if !ok {
				continue
			}
			imagePart := responses.ResponseInputImageContentParam{
				Detail:   responses.ResponseInputImageContentDetailAuto,
				ImageURL: officialopenai.String("data:" + image.MIMEType + ";base64," + image.Data),
			}
			content = append(content, responses.ResponseFunctionCallOutputItemUnionParam{OfInputImage: &imagePart})
		}
		return responses.ResponseInputItemParamOfFunctionCallOutput(callID, content), nil
	}
	if textResult.Len() == 0 {
		textResult.WriteString("(see attached image)")
	}
	return responses.ResponseInputItemParamOfFunctionCallOutput(callID, textResult.String()), nil
}

func mapResponsesTools(toolDefinitions []llm.Tool) ([]responses.ToolUnionParam, error) {
	mapped := make([]responses.ToolUnionParam, 0, len(toolDefinitions))
	for _, toolDefinition := range toolDefinitions {
		var parameterSchema map[string]any
		if err := json.Unmarshal(toolDefinition.Parameters, &parameterSchema); err != nil {
			return nil, fmt.Errorf("map tool %q parameters: %w", toolDefinition.Name, err)
		}
		definition := responses.FunctionToolParam{
			Name:       toolDefinition.Name,
			Parameters: parameterSchema,
			Strict:     officialopenai.Bool(toolDefinition.Strict),
		}
		if toolDefinition.Description != "" {
			definition.Description = officialopenai.String(toolDefinition.Description)
		}
		mapped = append(mapped, responses.ToolUnionParam{OfFunction: &definition})
	}
	return mapped, nil
}

func mapResponsesFormat(schemaFormat llm.JSONSchemaFormat) (responses.ResponseFormatTextConfigUnionParam, error) {
	var schema map[string]any
	if err := json.Unmarshal(schemaFormat.Schema, &schema); err != nil {
		return responses.ResponseFormatTextConfigUnionParam{}, fmt.Errorf("map response format %q schema: %w", schemaFormat.Name, err)
	}
	mapped := responses.ResponseFormatTextConfigParamOfJSONSchema(schemaFormat.Name, schema)
	mapped.OfJSONSchema.Strict = officialopenai.Bool(schemaFormat.Strict)
	if schemaFormat.Description != "" {
		mapped.OfJSONSchema.Description = officialopenai.String(schemaFormat.Description)
	}
	return mapped, nil
}

func splitResponsesToolCallID(toolCallID string) (string, string) {
	callID, itemID, found := strings.Cut(toolCallID, "|")
	if !found {
		return toolCallID, ""
	}
	return callID, itemID
}

func modelAccepts(targetModel llm.Model, modality llm.InputModality) bool {
	for _, supported := range targetModel.Input {
		if supported == modality {
			return true
		}
	}
	return false
}
