package openai

import (
	"context"
	"fmt"
	"net/http"

	"github.com/gorenx/goren/llm"
	officialopenai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
)

// NewResponses creates a model-bound OpenAI Responses adapter.
func NewResponses(targetModel llm.Model, httpClient *http.Client, adapterOptions ...AdapterOption) (llm.APIAdapter, error) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if err := llm.ValidateModel(targetModel); err != nil {
		return nil, err
	}
	configuration, err := resolveAdapterConfig(adapterOptions)
	if err != nil {
		return nil, err
	}
	if !configuration.systemRoleSet && targetModel.Reasoning {
		configuration.compat.SystemRole = SystemRoleDeveloper
	}
	if targetModel.API != llm.APIOpenAIResponses {
		return nil, fmt.Errorf(
			"%w: got %q, want %q",
			llm.ErrAPIMismatch,
			targetModel.API,
			llm.APIOpenAIResponses,
		)
	}
	return &responsesAdapter{
		targetModel: cloneModel(targetModel),
		sdkClient: officialopenai.NewClient(
			option.WithBaseURL(targetModel.BaseURL),
			option.WithHTTPClient(httpClient),
		),
		config: configuration,
	}, nil
}

// responsesAdapter maps one model-bound OpenAI Responses stream to the
// provider-neutral LLM event contract.
type responsesAdapter struct {
	targetModel llm.Model
	sdkClient   officialopenai.Client
	config      adapterConfig
}

func (*responsesAdapter) API() llm.API {
	return llm.APIOpenAIResponses
}

func (protocolAdapter *responsesAdapter) Stream(
	ctx context.Context,
	input llm.Context,
	invocationOptions llm.StreamOptions,
) (*llm.EventStream, error) {
	if err := llm.ValidateContext(input); err != nil {
		return nil, err
	}
	resolvedOptions, err := llm.ResolveStreamOptions(protocolAdapter.targetModel, invocationOptions)
	if err != nil {
		return nil, err
	}
	invocationOptions = resolvedOptions
	if err := llm.ValidateToolSelection(input.Tools, invocationOptions); err != nil {
		return nil, err
	}
	prepared, err := llm.PrepareContext(protocolAdapter.targetModel, input)
	if err != nil {
		return nil, err
	}
	input = prepared

	responseRequest, err := protocolAdapter.makeResponsesRequest(input, invocationOptions)
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
			protocolAdapter.runResponses(ctx, eventSink, responseRequest, input.Tools, invocationOptions)
		},
	), nil
}

func (protocolAdapter *responsesAdapter) runResponses(
	ctx context.Context,
	eventSink llm.StreamEmitter,
	responseRequest responses.ResponseNewParams,
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
	transformOptions, err := requestTransformOptions(requestContext, protocolAdapter.targetModel, invocationOptions, responseRequest)
	if err != nil {
		eventSink.Fail(err)
		return
	}
	var httpResponse *http.Response
	sdkStream := protocolAdapter.sdkClient.Responses.NewStreaming(
		requestContext,
		responseRequest,
		append(protocolAdapter.responsesRequestOptions(invocationOptions, &httpResponse), transformOptions...)...,
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
	responseAssembler := newResponsesState(
		protocolAdapter.targetModel,
		eventSink, toolDefinitions,
		invocationOptions.ServiceTier,
	)
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

func (protocolAdapter *responsesAdapter) responsesRequestOptions(
	invocationOptions llm.StreamOptions,
	httpResponse **http.Response,
) []option.RequestOption {
	opts := make([]option.RequestOption, 0, len(protocolAdapter.targetModel.Headers)+len(invocationOptions.Headers)+8)
	opts = append(opts, option.WithAPIKey(invocationOptions.APIKey))
	for name, value := range protocolAdapter.targetModel.Headers {
		opts = append(opts, option.WithHeader(name, value))
	}
	for name, value := range invocationOptions.Headers {
		opts = append(opts, option.WithHeader(name, value))
	}
	if invocationOptions.MaxRetries != nil {
		opts = append(opts, option.WithMaxRetries(*invocationOptions.MaxRetries))
	}
	if invocationOptions.Timeout > 0 {
		opts = append(opts, option.WithRequestTimeout(invocationOptions.Timeout))
	}
	if httpResponse != nil {
		opts = append(opts, option.WithResponseInto(httpResponse))
	}
	if invocationOptions.RequestID != "" {
		opts = append(opts, option.WithHeader("x-client-request-id", invocationOptions.RequestID))
	}
	if invocationOptions.SessionID != "" && (invocationOptions.CacheKey != "" || invocationOptions.CacheRetention != "") {
		for _, headerName := range protocolAdapter.config.compat.SessionAffinityHeaders {
			opts = append(opts, option.WithHeader(headerName, invocationOptions.SessionID))
		}
	}
	if invocationOptions.MaxRetryDelay > 0 {
		opts = append(opts, option.WithMiddleware(capRetryDelay(invocationOptions.MaxRetryDelay)))
	}
	if invocationOptions.ThinkingBudget > 0 && protocolAdapter.config.compat.ThinkingBudgetField != "" {
		opts = append(opts, option.WithJSONSet(protocolAdapter.config.compat.ThinkingBudgetField, invocationOptions.ThinkingBudget))
	}
	return opts
}

func (protocolAdapter *responsesAdapter) makeResponsesRequest(
	input llm.Context,
	invocationOptions llm.StreamOptions,
) (responses.ResponseNewParams, error) {
	outgoingItems, err := mapResponsesMessages(protocolAdapter.targetModel, input, protocolAdapter.config.compat)
	if err != nil {
		return responses.ResponseNewParams{}, err
	}
	responseRequest := responses.ResponseNewParams{
		Model: protocolAdapter.targetModel.ID,
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: outgoingItems,
		},
		Store: officialopenai.Bool(false),
	}
	if invocationOptions.ThinkingBudget > 0 && protocolAdapter.config.compat.ThinkingBudgetField == "" {
		return responses.ResponseNewParams{}, fmt.Errorf("OpenAI-compatible provider has no configured thinking budget field")
	}
	if invocationOptions.Temperature != nil {
		responseRequest.Temperature = officialopenai.Float(*invocationOptions.Temperature)
	}
	maxOutputTokens := invocationOptions.MaxOutputTokens
	if maxOutputTokens == 0 {
		maxOutputTokens = protocolAdapter.targetModel.MaxOutputTokens
	}
	if maxOutputTokens > 0 {
		responseRequest.MaxOutputTokens = officialopenai.Int(int64(maxOutputTokens))
	}
	if protocolAdapter.targetModel.Reasoning && invocationOptions.Reasoning != "" {
		_, mappedEffort, _, err := llm.ResolveReasoning(protocolAdapter.targetModel, invocationOptions.Reasoning)
		if err != nil {
			return responses.ResponseNewParams{}, err
		}
		summary := shared.ReasoningSummary(invocationOptions.ReasoningSummary)
		if summary == "" {
			summary = shared.ReasoningSummaryAuto
		}
		responseRequest.Reasoning = shared.ReasoningParam{
			Effort:  shared.ReasoningEffort(mappedEffort),
			Summary: summary,
		}
		responseRequest.Include = []responses.ResponseIncludable{
			responses.ResponseIncludableReasoningEncryptedContent,
		}
	}

	if invocationOptions.CacheKey != "" {
		responseRequest.PromptCacheKey = officialopenai.String(invocationOptions.CacheKey)
	}
	if invocationOptions.CacheRetention != "" {
		responseRequest.PromptCacheRetention = responses.ResponseNewParamsPromptCacheRetention(invocationOptions.CacheRetention)
	}
	if len(invocationOptions.Metadata) > 0 {
		responseRequest.Metadata = invocationOptions.Metadata
	}
	if invocationOptions.ServiceTier != "" {
		responseRequest.ServiceTier = responses.ResponseNewParamsServiceTier(invocationOptions.ServiceTier)
	}
	responseRequest.Tools, err = mapResponsesTools(input.Tools, protocolAdapter.config.compat)
	if err != nil {
		return responses.ResponseNewParams{}, err
	}
	if invocationOptions.ToolChoice != nil {
		if protocolAdapter.config.compat.DisableToolChoice {
			return responses.ResponseNewParams{}, fmt.Errorf("OpenAI-compatible provider does not support tool choice")
		}
		responseRequest.ToolChoice, err = mapResponsesToolChoice(*invocationOptions.ToolChoice)
		if err != nil {
			return responses.ResponseNewParams{}, err
		}
	}
	if invocationOptions.ResponseFormat != nil {
		responseRequest.Text.Format, err = mapResponsesFormat(*invocationOptions.ResponseFormat)
		if err != nil {
			return responses.ResponseNewParams{}, err
		}
	}
	return responseRequest, nil
}
