package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gorenx/goren/llm"
	"net/http"
	"strings"
	"time"

	officialopenai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

// Adapter implements the OpenAI-compatible Chat Completions wire protocol with
// the official OpenAI Go SDK.
type Adapter struct {
	client *http.Client
}

// New constructs an adapter using httpClient, or http.DefaultClient when nil.
func New(httpClient *http.Client) *Adapter {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Adapter{client: httpClient}
}

// API returns the wire protocol implemented by Adapter.
func (*Adapter) API() llm.API {
	return llm.APIOpenAICompletions
}

// Stream validates and maps an invocation, then lets the SDK run asynchronously
// inside a normalized goren event stream.
func (protocolAdapter *Adapter) Stream(
	ctx context.Context,
	targetModel llm.Model,
	input llm.Context,
	invocationOptions llm.StreamOptions,
) (*llm.EventStream, error) {
	if targetModel.API != protocolAdapter.API() {
		return nil, fmt.Errorf("%w: got %q, want %q", llm.ErrAPIMismatch, targetModel.API, protocolAdapter.API())
	}
	if err := llm.ValidateModel(targetModel); err != nil {
		return nil, err
	}
	if err := llm.ValidateContext(input); err != nil {
		return nil, err
	}
	if err := llm.ValidateOptions(invocationOptions); err != nil {
		return nil, err
	}

	completionRequest, err := makeRequest(targetModel, input, invocationOptions)
	if err != nil {
		return nil, err
	}

	return llm.NewEventStream(ctx, targetModel, func(ctx context.Context, eventSink llm.StreamEmitter) {
		if invocationOptions.APIKey == "" {
			eventSink.Fail(fmt.Errorf("no API key for provider %q", targetModel.Provider))
			return
		}
		protocolAdapter.run(ctx, eventSink, targetModel, completionRequest, invocationOptions)
	}), nil
}

func (protocolAdapter *Adapter) run(
	ctx context.Context,
	eventSink llm.StreamEmitter,
	targetModel llm.Model,
	completionRequest officialopenai.ChatCompletionNewParams,
	invocationOptions llm.StreamOptions,
) {
	sdkClient := officialopenai.NewClient(
		option.WithAPIKey(invocationOptions.APIKey),
		option.WithBaseURL(targetModel.BaseURL),
		option.WithHTTPClient(protocolAdapter.client),
	)
	sdkStream := sdkClient.Chat.Completions.NewStreaming(ctx, completionRequest, requestOptions(targetModel, invocationOptions)...)
	defer sdkStream.Close()

	if err := sdkStream.Err(); err != nil {
		finishTransportError(ctx, eventSink, err)
		return
	}

	eventSink.Emit(llm.StartEvent{})
	responseAssembler := newResponseState(targetModel, eventSink)
	for sdkStream.Next() {
		if err := responseAssembler.consume(sdkStream.Current()); err != nil {
			eventSink.Fail(err)
			return
		}
	}
	if err := sdkStream.Err(); err != nil {
		finishTransportError(ctx, eventSink, err)
		return
	}
	assistantReply, err := responseAssembler.finish()
	if err != nil {
		eventSink.Fail(err)
		return
	}
	eventSink.Done(assistantReply)
}

func requestOptions(targetModel llm.Model, invocationOptions llm.StreamOptions) []option.RequestOption {
	opts := make([]option.RequestOption, 0, len(targetModel.Headers)+len(invocationOptions.Headers))
	for name, value := range targetModel.Headers {
		opts = append(opts, option.WithHeader(name, value))
	}
	for name, value := range invocationOptions.Headers {
		opts = append(opts, option.WithHeader(name, value))
	}
	return opts
}

func finishTransportError(ctx context.Context, eventSink llm.StreamEmitter, err error) {
	if ctx.Err() != nil {
		eventSink.Abort(ctx.Err())
		return
	}
	eventSink.Fail(err)
}

func makeRequest(
	targetModel llm.Model,
	input llm.Context,
	invocationOptions llm.StreamOptions,
) (officialopenai.ChatCompletionNewParams, error) {
	outgoingMessages, err := mapMessages(input)
	if err != nil {
		return officialopenai.ChatCompletionNewParams{}, err
	}
	completionRequest := officialopenai.ChatCompletionNewParams{
		Model:    targetModel.ID,
		Messages: outgoingMessages,
		StreamOptions: officialopenai.ChatCompletionStreamOptionsParam{
			IncludeUsage: officialopenai.Bool(true),
		},
	}
	if invocationOptions.Temperature != nil {
		completionRequest.Temperature = officialopenai.Float(*invocationOptions.Temperature)
	}
	maxOutputTokens := invocationOptions.MaxOutputTokens
	if maxOutputTokens == 0 {
		maxOutputTokens = targetModel.MaxOutputTokens
	}
	if maxOutputTokens > 0 {
		completionRequest.MaxCompletionTokens = officialopenai.Int(int64(maxOutputTokens))
	}
	if invocationOptions.Reasoning != "" {
		completionRequest.ReasoningEffort = officialopenai.ReasoningEffort(invocationOptions.Reasoning)
	}

	completionRequest.Tools, err = mapTools(input.Tools)
	if err != nil {
		return officialopenai.ChatCompletionNewParams{}, err
	}
	if invocationOptions.ResponseFormat != nil {
		responseSchema, err := mapResponseFormat(*invocationOptions.ResponseFormat)
		if err != nil {
			return officialopenai.ChatCompletionNewParams{}, err
		}
		completionRequest.ResponseFormat.OfJSONSchema = &responseSchema
	}
	return completionRequest, nil
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

type responseBlock struct {
	kind      string
	signature string
	text      strings.Builder
	id        strings.Builder
	name      strings.Builder
	arguments strings.Builder
}

type responseState struct {
	model         llm.Model
	emitter       llm.StreamEmitter
	blocks        []*responseBlock
	text          *responseBlock
	thinking      *responseBlock
	tools         map[int]*responseBlock
	responseID    string
	responseModel string
	usage         llm.Usage
	stopReason    llm.StopReason
	finishError   string
	hasFinished   bool
}

func newResponseState(targetModel llm.Model, eventSink llm.StreamEmitter) *responseState {
	return &responseState{
		model:      targetModel,
		emitter:    eventSink,
		tools:      make(map[int]*responseBlock),
		stopReason: llm.StopReasonStop,
	}
}

func (responseAssembler *responseState) consume(chunk officialopenai.ChatCompletionChunk) error {
	if chunk.ID != "" {
		responseAssembler.responseID = chunk.ID
	}
	if chunk.Model != "" {
		responseAssembler.responseModel = chunk.Model
	}
	if chunk.JSON.Usage.Valid() {
		cacheRead := int(chunk.Usage.PromptTokensDetails.CachedTokens)
		cacheWrite := int(chunk.Usage.PromptTokensDetails.CacheWriteTokens)
		responseAssembler.usage = llm.Usage{
			InputTokens:      max(0, int(chunk.Usage.PromptTokens)-cacheRead-cacheWrite),
			OutputTokens:     int(chunk.Usage.CompletionTokens),
			CacheReadTokens:  cacheRead,
			CacheWriteTokens: cacheWrite,
		}
		responseAssembler.usage.TotalTokens = responseAssembler.usage.InputTokens + responseAssembler.usage.OutputTokens + cacheRead + cacheWrite
	}

	for _, choice := range chunk.Choices {
		reasoningText, signature, err := reasoningContent(choice.Delta)
		if err != nil {
			return err
		}
		if reasoningText != "" {
			index, contentBlock := responseAssembler.ensureThinking(signature)
			contentBlock.text.WriteString(reasoningText)
			responseAssembler.emitter.Emit(llm.ThinkingDeltaEvent{ContentIndex: index, Delta: reasoningText})
		}
		if choice.Delta.Content != "" {
			index, contentBlock := responseAssembler.ensureText()
			contentBlock.text.WriteString(choice.Delta.Content)
			responseAssembler.emitter.Emit(llm.TextDeltaEvent{ContentIndex: index, Delta: choice.Delta.Content})
		}
		for _, delta := range choice.Delta.ToolCalls {
			index, contentBlock, created := responseAssembler.ensureTool(int(delta.Index))
			if delta.ID != "" && contentBlock.id.Len() == 0 {
				contentBlock.id.WriteString(delta.ID)
			}
			if delta.Function.Name != "" {
				contentBlock.name.WriteString(delta.Function.Name)
			}
			if created {
				responseAssembler.emitter.Emit(llm.ToolCallStartEvent{
					ContentIndex: index,
					ID:           contentBlock.id.String(),
					Name:         contentBlock.name.String(),
				})
			}
			if delta.Function.Arguments != "" {
				contentBlock.arguments.WriteString(delta.Function.Arguments)
				responseAssembler.emitter.Emit(llm.ToolCallDeltaEvent{ContentIndex: index, Delta: delta.Function.Arguments})
			}
		}
		if choice.FinishReason != "" {
			responseAssembler.hasFinished = true
			responseAssembler.stopReason, responseAssembler.finishError = mapStopReason(choice.FinishReason)
		}
	}
	return nil
}

func reasoningContent(delta officialopenai.ChatCompletionChunkChoiceDelta) (string, string, error) {
	var extension map[string]json.RawMessage
	if raw := delta.RawJSON(); raw != "" {
		if err := json.Unmarshal([]byte(raw), &extension); err != nil {
			return "", "", fmt.Errorf("decode reasoning content: %w", err)
		}
	}
	for _, field := range []string{"reasoning_content", "reasoning", "reasoning_text"} {
		var content string
		if err := json.Unmarshal(extension[field], &content); err == nil && content != "" {
			return content, field, nil
		}
	}
	return "", "", nil
}

func (responseAssembler *responseState) ensureText() (int, *responseBlock) {
	if responseAssembler.text == nil {
		responseAssembler.text = &responseBlock{kind: "text"}
		responseAssembler.blocks = append(responseAssembler.blocks, responseAssembler.text)
		responseAssembler.emitter.Emit(llm.TextStartEvent{ContentIndex: len(responseAssembler.blocks) - 1})
	}
	return responseAssembler.blockIndex(responseAssembler.text), responseAssembler.text
}

func (responseAssembler *responseState) ensureThinking(signature string) (int, *responseBlock) {
	if responseAssembler.thinking == nil {
		responseAssembler.thinking = &responseBlock{kind: "thinking", signature: signature}
		responseAssembler.blocks = append(responseAssembler.blocks, responseAssembler.thinking)
		responseAssembler.emitter.Emit(llm.ThinkingStartEvent{ContentIndex: len(responseAssembler.blocks) - 1})
	}
	return responseAssembler.blockIndex(responseAssembler.thinking), responseAssembler.thinking
}

func (responseAssembler *responseState) ensureTool(toolIndex int) (int, *responseBlock, bool) {
	contentBlock := responseAssembler.tools[toolIndex]
	created := false
	if contentBlock == nil {
		contentBlock = &responseBlock{kind: "tool"}
		responseAssembler.tools[toolIndex] = contentBlock
		responseAssembler.blocks = append(responseAssembler.blocks, contentBlock)
		created = true
	}
	return responseAssembler.blockIndex(contentBlock), contentBlock, created
}

func (responseAssembler *responseState) blockIndex(contentBlock *responseBlock) int {
	for index, candidate := range responseAssembler.blocks {
		if candidate == contentBlock {
			return index
		}
	}
	return -1
}

func (responseAssembler *responseState) finish() (llm.AssistantMessage, error) {
	assistantReply := llm.AssistantMessage{
		API:           responseAssembler.model.API,
		Provider:      responseAssembler.model.Provider,
		Model:         responseAssembler.model.ID,
		ResponseModel: responseAssembler.responseModel,
		ResponseID:    responseAssembler.responseID,
		Usage:         responseAssembler.usage,
		StopReason:    responseAssembler.stopReason,
		Timestamp:     time.Now(),
	}
	assistantReply.Usage.Cost = responseAssembler.model.CalculateCost(assistantReply.Usage)
	for index, contentBlock := range responseAssembler.blocks {
		switch contentBlock.kind {
		case "text":
			visibleText := contentBlock.text.String()
			assistantReply.Content = append(assistantReply.Content, llm.TextContent{Text: visibleText})
			responseAssembler.emitter.Emit(llm.TextEndEvent{ContentIndex: index, Content: visibleText})
		case "thinking":
			thinkingText := contentBlock.text.String()
			assistantReply.Content = append(assistantReply.Content, llm.ThinkingContent{
				Thinking:  thinkingText,
				Signature: contentBlock.signature,
			})
			responseAssembler.emitter.Emit(llm.ThinkingEndEvent{ContentIndex: index, Content: thinkingText})
		case "tool":
			arguments := contentBlock.arguments.String()
			if arguments == "" {
				arguments = `{}`
			}
			if !json.Valid([]byte(arguments)) {
				return llm.AssistantMessage{}, fmt.Errorf("tool call %d has invalid streamed arguments", index)
			}
			assembledCall := llm.ToolCall{
				ID:        contentBlock.id.String(),
				Name:      contentBlock.name.String(),
				Arguments: json.RawMessage(arguments),
			}
			assistantReply.Content = append(assistantReply.Content, assembledCall)
			responseAssembler.emitter.Emit(llm.ToolCallEndEvent{ContentIndex: index, ToolCall: assembledCall})
		}
	}
	if len(responseAssembler.tools) > 0 && responseAssembler.stopReason == llm.StopReasonStop {
		assistantReply.StopReason = llm.StopReasonToolUse
	}
	if !responseAssembler.hasFinished {
		return assistantReply, errors.New("LLM stream ended without finish reason")
	}
	if responseAssembler.finishError != "" {
		return assistantReply, errors.New(responseAssembler.finishError)
	}
	return assistantReply, nil
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
