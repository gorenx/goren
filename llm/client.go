package llm

import (
	"context"
	"errors"
)

// APIKeyResolver resolves credentials by serving provider, independently of API routing.
type APIKeyResolver interface {
	APIKey(context.Context, Provider) (string, bool)
}

// APIKeyResolverFunc adapts a function to APIKeyResolver.
type APIKeyResolverFunc func(context.Context, Provider) (string, bool)

// APIKey calls resolve for provider.
func (keyResolver APIKeyResolverFunc) APIKey(ctx context.Context, servingProvider Provider) (string, bool) {
	return keyResolver(ctx, servingProvider)
}

// Client validates invocations and delegates them to one model-bound adapter.
type Client struct {
	targetProvider Provider
	adapter        APIAdapter
	keys           APIKeyResolver
}

// NewClient validates targetModel, resolves its registered constructor, and creates a
// model-bound adapter for the lifetime of the client.
func NewClient(targetModel Model, adapterRegistry *Registry, keys APIKeyResolver) (*Client, error) {
	if err := ValidateModel(targetModel); err != nil {
		return nil, err
	}
	if adapterRegistry == nil {
		return nil, errors.New("registry is required")
	}
	targetSnapshot := cloneModel(targetModel)
	constructAdapter, err := adapterRegistry.resolve(targetSnapshot.API)
	if err != nil {
		return nil, err
	}
	protocolAdapter, err := constructAdapter(targetSnapshot)
	if err != nil {
		return nil, err
	}
	if protocolAdapter == nil {
		return nil, errors.New("adapter constructor returned nil")
	}
	if protocolAdapter.API() != targetSnapshot.API {
		return nil, ErrAPIMismatch
	}
	return &Client{
		targetProvider: targetSnapshot.Provider,
		adapter:        protocolAdapter,
		keys:           keys,
	}, nil
}

func cloneModel(targetModel Model) Model {
	cloned := targetModel
	cloned.Input = append([]InputModality(nil), targetModel.Input...)
	if targetModel.Headers != nil {
		cloned.Headers = make(map[string]string, len(targetModel.Headers))
		for name, value := range targetModel.Headers {
			cloned.Headers[name] = value
		}
	}
	return cloned
}

// Stream validates and starts an invocation. Errors before a stream exists are
// returned directly; failures after return terminate the stream with ErrorEvent.
func (llmClient *Client) Stream(
	ctx context.Context,
	input Context,
	invocationOptions StreamOptions,
) (*EventStream, error) {
	if err := ValidateContext(input); err != nil {
		return nil, err
	}
	if err := ValidateOptions(invocationOptions); err != nil {
		return nil, err
	}
	if invocationOptions.APIKey == "" && llmClient.keys != nil {
		if key, ok := llmClient.keys.APIKey(ctx, llmClient.targetProvider); ok {
			invocationOptions.APIKey = key
		}
	}
	return llmClient.adapter.Stream(ctx, input, invocationOptions)
}

// Complete waits for the terminal assistant message without draining events.
// Runtime failure is reported in the returned message's stop reason, while err
// is reserved for startup failure or cancellation of the waiting context.
func (llmClient *Client) Complete(
	ctx context.Context,
	input Context,
	invocationOptions StreamOptions,
) (AssistantMessage, error) {
	responseStream, err := llmClient.Stream(ctx, input, invocationOptions)
	if err != nil {
		return AssistantMessage{}, err
	}
	return responseStream.Result(ctx)
}
