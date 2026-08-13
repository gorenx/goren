package openai

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/gorenx/goren/llm"
	officialopenai "github.com/openai/openai-go/v3"
)

func cloneModel(targetModel llm.Model) llm.Model {
	cloned := targetModel
	cloned.Input = append([]llm.InputModality(nil), targetModel.Input...)
	if targetModel.Headers != nil {
		cloned.Headers = make(map[string]string, len(targetModel.Headers))
		for name, value := range targetModel.Headers {
			cloned.Headers[name] = value
		}
	}
	return cloned
}

func mapMessages(input llm.Context) ([]officialopenai.ChatCompletionMessageParamUnion, error) {
	messages := make([]officialopenai.ChatCompletionMessageParamUnion, 0, len(input.Messages)+1)
	if input.SystemPrompt != "" {
		messages = append(messages, officialopenai.SystemMessage(input.SystemPrompt))
	}
	for index, conversationEntry := range input.Messages {
		switch value := conversationEntry.(type) {
		case llm.UserMessage:
			mapped, err := mapUserMessage(value)
			if err != nil {
				return nil, fmt.Errorf("map user message %d: %w", index, err)
			}
			messages = append(messages, mapped)
		case llm.AssistantMessage:
			mapped, err := mapAssistantMessage(value)
			if err != nil {
				return nil, fmt.Errorf("map assistant message %d: %w", index, err)
			}
			messages = append(messages, mapped)
		case llm.ToolResultMessage:
			resultText, err := mapToolResultContent(value.Content)
			if err != nil {
				return nil, fmt.Errorf("map tool result message %d: %w", index, err)
			}
			messages = append(messages, officialopenai.ToolMessage(resultText, value.ToolCallID))
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

func mapAssistantMessage(assistantInput llm.AssistantMessage) (officialopenai.ChatCompletionMessageParamUnion, error) {
	var visibleText strings.Builder
	mapped := officialopenai.ChatCompletionAssistantMessageParam{}
	for _, block := range assistantInput.Content {
		switch value := block.(type) {
		case llm.TextContent:
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
				return officialopenai.ChatCompletionMessageParamUnion{}, fmt.Errorf("tool call %q has invalid arguments", value.Name)
			}
			mapped.ToolCalls = append(mapped.ToolCalls, officialopenai.ChatCompletionMessageToolCallUnionParam{
				OfFunction: &officialopenai.ChatCompletionMessageFunctionToolCallParam{
					ID: value.ID,
					Function: officialopenai.ChatCompletionMessageFunctionToolCallFunctionParam{
						Name:      value.Name,
						Arguments: string(arguments),
					},
				},
			})
		default:
			return officialopenai.ChatCompletionMessageParamUnion{}, fmt.Errorf("unsupported assistant content %T", block)
		}
	}
	if visibleText.Len() > 0 {
		mapped.Content.OfString = officialopenai.String(visibleText.String())
	}
	return officialopenai.ChatCompletionMessageParamUnion{OfAssistant: &mapped}, nil
}

func mapToolResultContent(resultBlocks []llm.ToolResultContent) (string, error) {
	var resultText strings.Builder
	for _, block := range resultBlocks {
		switch value := block.(type) {
		case llm.TextContent:
			resultText.WriteString(value.Text)
		case llm.ImageContent:
			return "", errors.New("OpenAI Chat Completions tool results do not support portable image replay")
		default:
			return "", fmt.Errorf("unsupported tool result content %T", block)
		}
	}
	return resultText.String(), nil
}

func mapTools(toolDefinitions []llm.Tool) ([]officialopenai.ChatCompletionToolUnionParam, error) {
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
		if toolDefinition.Strict {
			definition.Strict = officialopenai.Bool(true)
		}
		mapped = append(mapped, officialopenai.ChatCompletionFunctionTool(definition))
	}
	return mapped, nil
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
