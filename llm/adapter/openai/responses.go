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
func NewResponses(targetModel llm.Model, httpClient *http.Client) (llm.APIAdapter, error) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if err := llm.ValidateModel(targetModel); err != nil {
		return nil, err
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
		httpClient:  httpClient,
	}, nil
}

// responsesAdapter maps one model-bound OpenAI Responses stream to the
// provider-neutral LLM event contract.
type responsesAdapter struct {
	targetModel llm.Model
	httpClient  *http.Client
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
	if err := llm.ValidateOptions(invocationOptions); err != nil {
		return nil, err
	}

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
			protocolAdapter.runResponses(ctx, eventSink, responseRequest, invocationOptions)
		},
	), nil
}

func (protocolAdapter *responsesAdapter) runResponses(
	ctx context.Context,
	eventSink llm.StreamEmitter,
	responseRequest responses.ResponseNewParams,
	invocationOptions llm.StreamOptions,
) {
	sdkClient := officialopenai.NewClient(
		option.WithAPIKey(invocationOptions.APIKey),
		option.WithBaseURL(protocolAdapter.targetModel.BaseURL),
		option.WithHTTPClient(protocolAdapter.httpClient),
	)
	sdkStream := sdkClient.Responses.NewStreaming(
		ctx,
		responseRequest,
		protocolAdapter.responsesRequestOptions(invocationOptions)...,
	)
	defer sdkStream.Close()

	if err := sdkStream.Err(); err != nil {
		finishTransportError(ctx, eventSink, err)
		return
	}

	eventSink.Emit(llm.StartEvent{})
	responseAssembler := newResponsesState(protocolAdapter.targetModel, eventSink)
	for sdkStream.Next() {
		if err := responseAssembler.consume(sdkStream.Current()); err != nil {
			if ctx.Err() != nil {
				eventSink.Abort(ctx.Err())
			} else {
				eventSink.Fail(err)
			}
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

func (protocolAdapter *responsesAdapter) responsesRequestOptions(
	invocationOptions llm.StreamOptions,
) []option.RequestOption {
	opts := make([]option.RequestOption, 0, len(protocolAdapter.targetModel.Headers)+len(invocationOptions.Headers))
	for name, value := range protocolAdapter.targetModel.Headers {
		opts = append(opts, option.WithHeader(name, value))
	}
	for name, value := range invocationOptions.Headers {
		opts = append(opts, option.WithHeader(name, value))
	}
	return opts
}

func (protocolAdapter *responsesAdapter) makeResponsesRequest(
	input llm.Context,
	invocationOptions llm.StreamOptions,
) (responses.ResponseNewParams, error) {
	outgoingItems, err := mapResponsesMessages(protocolAdapter.targetModel, input)
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
	if maxOutputTokens == 0 {
		maxOutputTokens = protocolAdapter.targetModel.MaxOutputTokens
	}
	if maxOutputTokens > 0 {
		responseRequest.MaxOutputTokens = officialopenai.Int(int64(maxOutputTokens))
	}
	if invocationOptions.Reasoning != "" {
		responseRequest.Reasoning = shared.ReasoningParam{
			Effort:  shared.ReasoningEffort(invocationOptions.Reasoning),
			Summary: shared.ReasoningSummaryAuto,
		}
		responseRequest.Include = []responses.ResponseIncludable{
			responses.ResponseIncludableReasoningEncryptedContent,
		}
	}

	responseRequest.Tools, err = mapResponsesTools(input.Tools)
	if err != nil {
		return responses.ResponseNewParams{}, err
	}
	if invocationOptions.ResponseFormat != nil {
		responseRequest.Text.Format, err = mapResponsesFormat(*invocationOptions.ResponseFormat)
		if err != nil {
			return responses.ResponseNewParams{}, err
		}
	}
	return responseRequest, nil
}
