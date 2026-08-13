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
func New(targetModel llm.Model, httpClient *http.Client) (llm.APIAdapter, error) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	protocolAdapter, err := newAdapter(targetModel, httpClient)
	if err != nil {
		return nil, err
	}
	return protocolAdapter, nil
}

// adapter implements one model-bound OpenAI-compatible Chat Completions client
// with the official OpenAI Go SDK.
type adapter struct {
	targetModel llm.Model
	httpClient  *http.Client
}

func newAdapter(targetModel llm.Model, httpClient *http.Client) (*adapter, error) {
	if err := llm.ValidateModel(targetModel); err != nil {
		return nil, err
	}
	if targetModel.API != llm.APIOpenAICompletions {
		return nil, fmt.Errorf(
			"%w: got %q, want %q",
			llm.ErrAPIMismatch,
			targetModel.API,
			llm.APIOpenAICompletions,
		)
	}
	return &adapter{
		targetModel: cloneModel(targetModel),
		httpClient:  httpClient,
	}, nil
}

func (*adapter) API() llm.API {
	return llm.APIOpenAICompletions
}

// Stream validates and maps an invocation, then lets the SDK run asynchronously
// inside a normalized LLM event stream.
func (protocolAdapter *adapter) Stream(
	ctx context.Context,
	input llm.Context,
	invocationOptions llm.StreamOptions,
) (*llm.EventStream, error) {
	if err := llm.ValidateContext(input); err != nil {
		return nil, err
	}
	if err := llm.ValidateOptions(invocationOptions); err != nil {
		return nil, err
	}

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
			protocolAdapter.run(ctx, eventSink, completionRequest, invocationOptions)
		},
	), nil
}

func (protocolAdapter *adapter) run(
	ctx context.Context,
	eventSink llm.StreamEmitter,
	completionRequest officialopenai.ChatCompletionNewParams,
	invocationOptions llm.StreamOptions,
) {
	sdkClient := officialopenai.NewClient(
		option.WithAPIKey(invocationOptions.APIKey),
		option.WithBaseURL(protocolAdapter.targetModel.BaseURL),
		option.WithHTTPClient(protocolAdapter.httpClient),
	)
	sdkStream := sdkClient.Chat.Completions.NewStreaming(
		ctx,
		completionRequest,
		protocolAdapter.requestOptions(invocationOptions)...,
	)
	defer sdkStream.Close()

	if err := sdkStream.Err(); err != nil {
		finishTransportError(ctx, eventSink, err)
		return
	}

	eventSink.Emit(llm.StartEvent{})
	responseAssembler := newResponseState(protocolAdapter.targetModel, eventSink)
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

func (protocolAdapter *adapter) requestOptions(invocationOptions llm.StreamOptions) []option.RequestOption {
	opts := make([]option.RequestOption, 0, len(protocolAdapter.targetModel.Headers)+len(invocationOptions.Headers))
	for name, value := range protocolAdapter.targetModel.Headers {
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

func (protocolAdapter *adapter) makeRequest(
	input llm.Context,
	invocationOptions llm.StreamOptions,
) (officialopenai.ChatCompletionNewParams, error) {
	outgoingMessages, err := mapMessages(input)
	if err != nil {
		return officialopenai.ChatCompletionNewParams{}, err
	}
	completionRequest := officialopenai.ChatCompletionNewParams{
		Model:    protocolAdapter.targetModel.ID,
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
		maxOutputTokens = protocolAdapter.targetModel.MaxOutputTokens
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
