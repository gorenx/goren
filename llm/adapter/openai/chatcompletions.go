package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gorenx/goren/llm"
	officialopenai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

// New creates a model-bound OpenAI-compatible Chat Completions adapter.
func New(targetModel llm.Model, httpClient *http.Client, adapterOptions ...AdapterOption) (llm.APIAdapter, error) {
	if err := validateAdapterModel(targetModel, llm.APIOpenAICompletions); err != nil {
		return nil, err
	}
	configuration, err := resolveAdapterConfig(adapterOptions)
	if err != nil {
		return nil, err
	}
	return &adapter{
		targetModel: cloneModel(targetModel),
		sdkClient:   newSDKClient(targetModel, httpClient),
		config:      configuration,
	}, nil
}

// adapter implements one model-bound OpenAI-compatible Chat Completions client
// with the official OpenAI Go SDK.
type adapter struct {
	targetModel llm.Model
	sdkClient   officialopenai.Client
	config      adapterConfig
}

func (*adapter) API() llm.API {
	return llm.APIOpenAICompletions
}

// Stream maps Client-prepared input, then lets the SDK run asynchronously
// inside a normalized LLM event stream.
func (protocolAdapter *adapter) Stream(
	ctx context.Context,
	input llm.Context,
	invocationOptions llm.StreamOptions,
) (*llm.EventStream, error) {
	completionRequest, err := protocolAdapter.makeRequest(input, invocationOptions)
	if err != nil {
		return nil, err
	}
	return llm.NewEventStream(
		ctx, protocolAdapter.targetModel,
		func(ctx context.Context, eventSink llm.StreamEmitter) {
			if invocationOptions.APIKey == "" {
				eventSink.Fail(fmt.Errorf("no API key for provider %q", protocolAdapter.targetModel.Provider))
				return
			}
			protocolAdapter.run(ctx, eventSink, completionRequest, input.Tools, invocationOptions)
		},
	), nil
}

func (protocolAdapter *adapter) run(
	ctx context.Context,
	eventSink llm.StreamEmitter,
	completionRequest officialopenai.ChatCompletionNewParams,
	toolDefinitions []llm.Tool,
	invocationOptions llm.StreamOptions,
) {
	requestContext := ctx
	if invocationOptions.Timeout > 0 {
		var cancel context.CancelFunc
		requestContext, cancel = context.WithTimeout(ctx, invocationOptions.Timeout)
		defer cancel()
	}
	if err := runBeforeRequest(requestContext, protocolAdapter.targetModel, invocationOptions); err != nil {
		eventSink.Fail(err)
		return
	}
	transformOptions, err := requestTransformOptions(requestContext, protocolAdapter.targetModel, invocationOptions, completionRequest)
	if err != nil {
		eventSink.Fail(err)
		return
	}
	var httpResponse *http.Response
	sdkStream := protocolAdapter.sdkClient.Chat.Completions.NewStreaming(
		requestContext,
		completionRequest,
		append(protocolAdapter.requestOptions(invocationOptions, &httpResponse), transformOptions...)...,
	)
	defer sdkStream.Close()

	if err := sdkStream.Err(); err != nil {
		finishTransportError(requestContext, eventSink, err)
		return
	}
	if err := runAfterResponse(requestContext, invocationOptions, httpResponse); err != nil {
		eventSink.Fail(err)
		return
	}

	eventSink.Emit(llm.StartEvent{})
	responseAssembler := newResponseState(protocolAdapter.targetModel, eventSink, toolDefinitions, invocationOptions.ServiceTier)
	for sdkStream.Next() {
		if err := responseAssembler.consume(sdkStream.Current()); err != nil {
			finishStateError(requestContext, eventSink, responseAssembler.snapshot(), err)
			return
		}
	}
	if err := sdkStream.Err(); err != nil {
		finishStateError(requestContext, eventSink, responseAssembler.snapshot(), err)
		return
	}
	assistantReply, err := responseAssembler.finish()
	if err != nil {
		finishStateError(requestContext, eventSink, assistantReply, err)
		return
	}
	eventSink.Done(assistantReply)
}

func (protocolAdapter *adapter) requestOptions(
	invocationOptions llm.StreamOptions,
	httpResponse **http.Response,
) []option.RequestOption {
	opts := transportRequestOptions(protocolAdapter.targetModel, protocolAdapter.config.compat, invocationOptions, httpResponse)
	if protocolAdapter.targetModel.Reasoning && invocationOptions.Reasoning != "" {
		_, mappedEffort, _, _ := llm.ResolveReasoning(protocolAdapter.targetModel, invocationOptions.Reasoning)
		switch protocolAdapter.config.compat.ReasoningFormat {
		case ReasoningFormatOpenRouter:
			opts = append(opts, option.WithJSONSet("reasoning.effort", mappedEffort))
		case ReasoningFormatDeepSeek:
			thinkingType := "enabled"
			if invocationOptions.Reasoning == llm.ReasoningOff {
				thinkingType = "disabled"
				opts = append(opts, option.WithJSONSet("thinking.type", thinkingType))
				break
			}
			opts = append(opts,
				option.WithJSONSet("thinking.type", thinkingType),
				option.WithJSONSet("reasoning_effort", mappedEffort),
			)
		case ReasoningFormatQwen:
			opts = append(opts, option.WithJSONSet("enable_thinking", invocationOptions.Reasoning != llm.ReasoningOff))
		}
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

func finishStateError(ctx context.Context, eventSink llm.StreamEmitter, assistantReply llm.AssistantMessage, err error) {
	if ctx.Err() != nil {
		eventSink.AbortWith(assistantReply, ctx.Err())
		return
	}
	eventSink.FailWith(assistantReply, err)
}

func (protocolAdapter *adapter) makeRequest(
	input llm.Context,
	invocationOptions llm.StreamOptions,
) (officialopenai.ChatCompletionNewParams, error) {
	if err := validateCompatibleInvocation(protocolAdapter.config.compat, invocationOptions); err != nil {
		return officialopenai.ChatCompletionNewParams{}, err
	}
	outgoingMessages, err := mapMessages(protocolAdapter.targetModel, input, protocolAdapter.config.compat)
	if err != nil {
		return officialopenai.ChatCompletionNewParams{}, err
	}
	completionRequest := officialopenai.ChatCompletionNewParams{
		Model:    protocolAdapter.targetModel.ID,
		Messages: outgoingMessages,
	}
	if !protocolAdapter.config.compat.DisableStreamingUsage {
		completionRequest.StreamOptions = officialopenai.ChatCompletionStreamOptionsParam{
			IncludeUsage: officialopenai.Bool(true),
		}
	}
	if invocationOptions.Temperature != nil {
		completionRequest.Temperature = officialopenai.Float(*invocationOptions.Temperature)
	}
	maxOutputTokens := invocationOptions.MaxOutputTokens
	if maxOutputTokens > 0 {
		if protocolAdapter.config.compat.MaxTokensField == MaxTokensLegacy {
			completionRequest.MaxTokens = officialopenai.Int(int64(maxOutputTokens))
		} else {
			completionRequest.MaxCompletionTokens = officialopenai.Int(int64(maxOutputTokens))
		}
	}
	if invocationOptions.ReasoningSummary != "" {
		return officialopenai.ChatCompletionNewParams{}, errors.New("OpenAI Chat Completions does not support reasoning summary")
	}
	if protocolAdapter.targetModel.Reasoning && invocationOptions.Reasoning != "" && protocolAdapter.config.compat.ReasoningFormat == ReasoningFormatOpenAI {
		_, mappedEffort, _, err := llm.ResolveReasoning(protocolAdapter.targetModel, invocationOptions.Reasoning)
		if err != nil {
			return officialopenai.ChatCompletionNewParams{}, err
		}
		completionRequest.ReasoningEffort = officialopenai.ReasoningEffort(mappedEffort)
	}
	if invocationOptions.CacheKey != "" {
		completionRequest.PromptCacheKey = officialopenai.String(invocationOptions.CacheKey)
	}
	if invocationOptions.CacheRetention != "" {
		completionRequest.PromptCacheRetention = officialopenai.ChatCompletionNewParamsPromptCacheRetention(invocationOptions.CacheRetention)
	}
	if len(invocationOptions.Metadata) > 0 {
		completionRequest.Metadata = invocationOptions.Metadata
	}
	if invocationOptions.ServiceTier != "" {
		completionRequest.ServiceTier = officialopenai.ChatCompletionNewParamsServiceTier(invocationOptions.ServiceTier)
	}

	completionRequest.Tools, err = mapTools(input.Tools, protocolAdapter.config.compat)
	if err != nil {
		return officialopenai.ChatCompletionNewParams{}, err
	}
	if invocationOptions.ToolChoice != nil {
		completionRequest.ToolChoice, err = mapChatToolChoice(*invocationOptions.ToolChoice)
		if err != nil {
			return officialopenai.ChatCompletionNewParams{}, err
		}
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

type responseBlock struct {
	kind      string
	signature string
	text      strings.Builder
	id        strings.Builder
	name      strings.Builder
	arguments strings.Builder
	closed    bool
}

type responseState struct {
	target          llm.Model
	emitter         llm.StreamEmitter
	blocks          []*responseBlock
	text            *responseBlock
	thinking        *responseBlock
	tools           map[int]*responseBlock
	responseID      string
	responseModel   string
	tokenUsage      llm.Usage
	stopReason      llm.StopReason
	finishError     string
	hasFinished     bool
	toolDefinitions []llm.Tool
	serviceTier     string
}

func newResponseState(
	targetModel llm.Model,
	eventSink llm.StreamEmitter,
	toolDefinitions []llm.Tool,
	serviceTier string,
) *responseState {
	return &responseState{
		target:          targetModel,
		emitter:         eventSink,
		tools:           make(map[int]*responseBlock),
		toolDefinitions: toolDefinitions,
		stopReason:      llm.StopReasonStop,
		serviceTier:     serviceTier,
	}
}

func (responseAssembler *responseState) consume(chunk officialopenai.ChatCompletionChunk) error {
	defer func() { responseAssembler.emitter.Update(responseAssembler.snapshot()) }()
	if chunk.ID != "" {
		responseAssembler.responseID = chunk.ID
	}
	if chunk.Model != "" {
		responseAssembler.responseModel = chunk.Model
	}
	if chunk.ServiceTier != "" {
		responseAssembler.serviceTier = string(chunk.ServiceTier)
	}
	if chunk.JSON.Usage.Valid() {
		cacheRead := int(chunk.Usage.PromptTokensDetails.CachedTokens)
		cacheWrite := int(chunk.Usage.PromptTokensDetails.CacheWriteTokens)
		responseAssembler.tokenUsage = llm.Usage{
			InputTokens:      max(0, int(chunk.Usage.PromptTokens)-cacheRead-cacheWrite),
			OutputTokens:     int(chunk.Usage.CompletionTokens),
			CacheReadTokens:  cacheRead,
			CacheWriteTokens: cacheWrite,
		}
		responseAssembler.tokenUsage.TotalTokens = responseAssembler.tokenUsage.InputTokens + responseAssembler.tokenUsage.OutputTokens + cacheRead + cacheWrite
	}

	for _, choice := range chunk.Choices {
		thinkingDelta, signature, err := reasoningContent(choice.Delta)
		if err != nil {
			return err
		}
		if thinkingDelta != "" {
			index, contentBlock := responseAssembler.ensureThinking(signature)
			contentBlock.text.WriteString(thinkingDelta)
			responseAssembler.emit(llm.ThinkingDeltaEvent{ContentIndex: index, Delta: thinkingDelta})
		}
		if choice.Delta.Content != "" {
			index, contentBlock := responseAssembler.ensureText()
			contentBlock.text.WriteString(choice.Delta.Content)
			responseAssembler.emit(llm.TextDeltaEvent{ContentIndex: index, Delta: choice.Delta.Content})
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
				responseAssembler.emit(llm.ToolCallStartEvent{
					ContentIndex: index,
					ID:           contentBlock.id.String(),
					Name:         contentBlock.name.String(),
				})
			}
			if delta.Function.Arguments != "" {
				contentBlock.arguments.WriteString(delta.Function.Arguments)
				responseAssembler.emit(llm.ToolCallDeltaEvent{ContentIndex: index, Delta: delta.Function.Arguments})
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
		responseAssembler.emit(llm.TextStartEvent{ContentIndex: len(responseAssembler.blocks) - 1})
	}
	return responseAssembler.blockIndex(responseAssembler.text), responseAssembler.text
}

func (responseAssembler *responseState) ensureThinking(signature string) (int, *responseBlock) {
	if responseAssembler.thinking == nil {
		responseAssembler.thinking = &responseBlock{kind: "thinking", signature: signature}
		responseAssembler.blocks = append(responseAssembler.blocks, responseAssembler.thinking)
		responseAssembler.emit(llm.ThinkingStartEvent{ContentIndex: len(responseAssembler.blocks) - 1})
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
	for index, contentBlock := range responseAssembler.blocks {
		if contentBlock.kind == "tool" {
			arguments := contentBlock.arguments.String()
			if arguments == "" {
				arguments = `{}`
			}
			if !json.Valid([]byte(arguments)) {
				return responseAssembler.snapshot(), fmt.Errorf("tool call %d has invalid streamed arguments", index)
			}
		}
		contentBlock.closed = true
	}
	assistantReply := responseAssembler.snapshot()
	for index, contentBlock := range responseAssembler.blocks {
		switch contentBlock.kind {
		case "text":
			responseAssembler.emit(llm.TextEndEvent{ContentIndex: index, Content: contentBlock.text.String()})
		case "thinking":
			responseAssembler.emit(llm.ThinkingEndEvent{ContentIndex: index, Content: contentBlock.text.String()})
		case "tool":
			arguments := contentBlock.arguments.String()
			if arguments == "" {
				arguments = `{}`
			}
			assembledCall := llm.ToolCall{ID: contentBlock.id.String(), Name: contentBlock.name.String(), Arguments: json.RawMessage(arguments)}
			responseAssembler.emit(llm.ToolCallEndEvent{ContentIndex: index, ToolCall: assembledCall})
		}
	}
	if len(responseAssembler.tools) > 0 && assistantReply.StopReason == llm.StopReasonStop {
		assistantReply.StopReason = llm.StopReasonToolUse
	}
	if !responseAssembler.hasFinished {
		return assistantReply, errors.New("LLM stream ended without finish reason")
	}
	if responseAssembler.finishError != "" {
		return assistantReply, errors.New(responseAssembler.finishError)
	}
	if err := llm.ValidateAssistantToolCalls(responseAssembler.toolDefinitions, assistantReply); err != nil {
		return assistantReply, err
	}
	return assistantReply, nil
}

func (responseAssembler *responseState) snapshot() llm.AssistantMessage {
	assistantReply := llm.AssistantMessage{
		API:           responseAssembler.target.API,
		Provider:      responseAssembler.target.Provider,
		Model:         responseAssembler.target.ID,
		ResponseModel: responseAssembler.responseModel,
		ResponseID:    responseAssembler.responseID,
		Usage:         responseAssembler.tokenUsage,
		StopReason:    responseAssembler.stopReason,
		Timestamp:     time.Now(),
	}
	assistantReply.Usage.ServiceTier = responseAssembler.serviceTier
	assistantReply.Usage.Cost = responseAssembler.target.CalculateCostForTier(
		assistantReply.Usage, responseAssembler.serviceTier)
	for _, contentBlock := range responseAssembler.blocks {
		switch contentBlock.kind {
		case "text":
			visibleText := contentBlock.text.String()
			assistantReply.Content = append(assistantReply.Content,
				llm.AssistantTextContent{Text: visibleText, Phase: llm.AssistantTextPhaseUnspecified},
			)
		case "thinking":
			thinkingText := contentBlock.text.String()
			assistantReply.Content = append(assistantReply.Content, llm.ThinkingContent{
				Thinking:  thinkingText,
				Signature: contentBlock.signature,
			})
		case "tool":
			arguments := contentBlock.arguments.String()
			var completedArguments json.RawMessage
			if arguments == "" && contentBlock.closed {
				completedArguments = json.RawMessage(`{}`)
			} else if json.Valid([]byte(arguments)) {
				completedArguments = json.RawMessage(arguments)
			}
			assembledCall := llm.ToolCall{
				ID:        contentBlock.id.String(),
				Name:      contentBlock.name.String(),
				Arguments: completedArguments,
			}
			assistantReply.Content = append(assistantReply.Content, assembledCall)
		}
	}
	if len(responseAssembler.tools) > 0 && assistantReply.StopReason == llm.StopReasonStop {
		assistantReply.StopReason = llm.StopReasonToolUse
	}
	return assistantReply
}

func (responseAssembler *responseState) emit(streamEvent llm.Event) bool {
	responseAssembler.emitter.Update(responseAssembler.snapshot())
	return responseAssembler.emitter.Emit(streamEvent)
}
