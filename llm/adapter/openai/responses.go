package openai

import (
	"context"
	"fmt"
	"net/http"

	"github.com/gorenx/goren/llm"
	officialopenai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
)

// NewResponses creates a model-bound OpenAI Responses adapter.
func NewResponses(targetModel llm.Model, httpClient *http.Client, adapterOptions ...AdapterOption) (llm.APIAdapter, error) {
	if err := validateAdapterModel(targetModel, llm.APIOpenAIResponses); err != nil {
		return nil, err
	}
	configuration, err := resolveAdapterConfig(adapterOptions)
	if err != nil {
		return nil, err
	}
	if !configuration.systemRoleSet && targetModel.Reasoning {
		configuration.compat.SystemRole = SystemRoleDeveloper
	}
	return &responsesAdapter{
		targetModel: cloneModel(targetModel),
		sdkClient:   newSDKClient(targetModel, httpClient),
		config:      configuration,
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
		append(transportRequestOptions(protocolAdapter.targetModel, protocolAdapter.config.compat, invocationOptions, &httpResponse), transformOptions...)...,
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

func (protocolAdapter *responsesAdapter) makeResponsesRequest(
	input llm.Context,
	invocationOptions llm.StreamOptions,
) (responses.ResponseNewParams, error) {
	if err := validateCompatibleInvocation(protocolAdapter.config.compat, invocationOptions); err != nil {
		return responses.ResponseNewParams{}, err
	}
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
	if invocationOptions.Temperature != nil {
		responseRequest.Temperature = officialopenai.Float(*invocationOptions.Temperature)
	}
	maxOutputTokens := invocationOptions.MaxOutputTokens
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
