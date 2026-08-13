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

func mapResponsesMessages(targetModel llm.Model, input llm.Context, compatibleBehavior Compatibility) (responses.ResponseInputParam, error) {
	items := make(responses.ResponseInputParam, 0, len(input.Messages)+1)
	toolCallIDs := make(map[string]string)
	if input.SystemPrompt != "" {
		role := responses.EasyInputMessageRoleSystem
		if compatibleBehavior.SystemRole == SystemRoleDeveloper {
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
			mapped, err := mapResponsesAssistantMessage(targetModel, value, messageIndex)
			if err != nil {
				return nil, fmt.Errorf("map assistant message %d: %w", messageIndex, err)
			}
			items = append(items, mapped...)
			for _, contentBlock := range value.Content {
				if requestedCall, ok := contentBlock.(llm.ToolCall); ok {
					callID, _, err := responsesToolCallIdentity(requestedCall, targetModel, value)
					if err != nil {
						return nil, fmt.Errorf("map assistant message %d: %w", messageIndex, err)
					}
					toolCallIDs[requestedCall.ID] = callID
				}
			}
		case llm.ToolResultMessage:
			callID := toolCallIDs[value.ToolCallID]
			if callID == "" {
				callID = value.ToolCallID
			}
			mapped, err := mapResponsesToolResult(targetModel, value, callID, compatibleBehavior)
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
	targetModel llm.Model,
	assistantInput llm.AssistantMessage,
	messageIndex int,
) ([]responses.ResponseInputItemUnionParam, error) {
	items := make([]responses.ResponseInputItemUnionParam, 0, len(assistantInput.Content))
	textIndex := 0
	for _, block := range assistantInput.Content {
		switch value := block.(type) {
		case llm.ThinkingContent:
			replayData := ""
			if sameResponsesModel(targetModel, assistantInput) && value.Signature != "" {
				replayData = value.Signature
			}
			if sameResponsesModel(targetModel, assistantInput) && value.Metadata != nil && value.Metadata.API == targetModel.API && value.Metadata.Provider == targetModel.Provider && value.Metadata.Model == targetModel.ID {
				replayData = string(value.Metadata.Data)
			}
			if replayData == "" {
				continue
			}
			var reasoning responses.ResponseReasoningItemParam
			if err := json.Unmarshal([]byte(replayData), &reasoning); err != nil {
				return nil, fmt.Errorf("decode reasoning replay metadata: %w", err)
			}
			items = append(items, responses.ResponseInputItemUnionParam{OfReasoning: &reasoning})
		case llm.AssistantTextContent:
			messageID := fmt.Sprintf("msg_goren_%d_%d", messageIndex, textIndex)
			textIndex++
			replayID, err := responsesReplayItemID(value.Metadata, targetModel, assistantInput)
			if err != nil {
				return nil, err
			}
			if replayID != "" {
				messageID = replayID
			}
			outputText := responses.ResponseOutputTextParam{
				Text:        value.Text,
				Annotations: []responses.ResponseOutputTextAnnotationUnionParam{},
			}
			message := responses.ResponseInputItemParamOfOutputMessage(
				[]responses.ResponseOutputMessageContentUnionParam{{OfOutputText: &outputText}},
				messageID,
				responses.ResponseOutputMessageStatusCompleted,
			)
			if value.Phase == llm.AssistantTextPhaseCommentary || value.Phase == llm.AssistantTextPhaseFinalAnswer {
				message.OfOutputMessage.Phase = responses.ResponseOutputMessagePhase(value.Phase)
			}
			items = append(items, message)
		case llm.ToolCall:
			arguments := value.Arguments
			if len(arguments) == 0 {
				arguments = json.RawMessage(`{}`)
			}
			if !json.Valid(arguments) {
				return nil, fmt.Errorf("tool call %q has invalid arguments", value.Name)
			}
			callID, itemID, err := responsesToolCallIdentity(value, targetModel, assistantInput)
			if err != nil {
				return nil, err
			}
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
	callID string,
	compatibleBehavior Compatibility,
) (responses.ResponseInputItemUnionParam, error) {
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
	if toolResult.IsError {
		originalText := textResult.String()
		textResult.Reset()
		textResult.WriteString(compatibleBehavior.ToolErrorPrefix)
		textResult.WriteString(originalText)
	}
	if hasImages && !modelAccepts(targetModel, llm.InputImage) {
		return responses.ResponseInputItemUnionParam{}, fmt.Errorf("%w: image", llm.ErrUnsupportedModality)
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
	return responses.ResponseInputItemParamOfFunctionCallOutput(callID, textResult.String()), nil
}

func responsesToolCallIdentity(requestedCall llm.ToolCall, targetModel llm.Model, assistantInput llm.AssistantMessage) (string, string, error) {
	replayID, err := responsesReplayItemID(requestedCall.Metadata, targetModel, assistantInput)
	if err != nil {
		return "", "", err
	}
	if replayID != "" {
		return requestedCall.ID, replayID, nil
	}
	callID, itemID := splitResponsesToolCallID(requestedCall.ID)
	return callID, itemID, nil
}

func mapResponsesTools(toolDefinitions []llm.Tool, compatibleBehavior Compatibility) ([]responses.ToolUnionParam, error) {
	mapped := make([]responses.ToolUnionParam, 0, len(toolDefinitions))
	for _, toolDefinition := range toolDefinitions {
		var parameterSchema map[string]any
		if err := json.Unmarshal(toolDefinition.Parameters, &parameterSchema); err != nil {
			return nil, fmt.Errorf("map tool %q parameters: %w", toolDefinition.Name, err)
		}
		definition := responses.FunctionToolParam{
			Name:       toolDefinition.Name,
			Parameters: parameterSchema,
		}
		if !compatibleBehavior.DisableStrictTools {
			definition.Strict = officialopenai.Bool(toolDefinition.Strict)
		}
		if toolDefinition.Description != "" {
			definition.Description = officialopenai.String(toolDefinition.Description)
		}
		mapped = append(mapped, responses.ToolUnionParam{OfFunction: &definition})
	}
	return mapped, nil
}

func mapResponsesToolChoice(toolSelection llm.ToolChoice) (responses.ResponseNewParamsToolChoiceUnion, error) {
	switch toolSelection.Mode {
	case llm.ToolChoiceAuto, llm.ToolChoiceNone, llm.ToolChoiceRequired:
		var mapped responses.ResponseNewParamsToolChoiceUnion
		if err := json.Unmarshal([]byte(`"`+string(toolSelection.Mode)+`"`), &mapped); err != nil {
			return responses.ResponseNewParamsToolChoiceUnion{}, fmt.Errorf("map tool choice: %w", err)
		}
		return mapped, nil
	case llm.ToolChoiceFunction:
		return responses.ResponseNewParamsToolChoiceUnion{
			OfFunctionTool: &responses.ToolChoiceFunctionParam{Name: toolSelection.Name},
		}, nil
	default:
		return responses.ResponseNewParamsToolChoiceUnion{}, fmt.Errorf("unsupported tool choice %q", toolSelection.Mode)
	}
}

type replayItemMetadata struct {
	ItemID string `json:"item_id"`
}

func responsesReplayItemID(metadata *llm.ReplayMetadata, targetModel llm.Model, assistantInput llm.AssistantMessage) (string, error) {
	if metadata == nil || !sameResponsesModel(targetModel, assistantInput) || metadata.API != targetModel.API || metadata.Provider != targetModel.Provider || metadata.Model != targetModel.ID {
		return "", nil
	}
	var replay replayItemMetadata
	if err := json.Unmarshal(metadata.Data, &replay); err != nil {
		return "", fmt.Errorf("decode Responses replay metadata: %w", err)
	}
	if replay.ItemID == "" {
		return "", errors.New("Responses replay metadata has no item ID")
	}
	return replay.ItemID, nil
}

func sameResponsesModel(targetModel llm.Model, assistantInput llm.AssistantMessage) bool {
	return assistantInput.API == targetModel.API && assistantInput.Provider == targetModel.Provider && assistantInput.Model == targetModel.ID
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
