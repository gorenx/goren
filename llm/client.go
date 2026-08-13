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

// Client validates invocations and routes models to adapters by Model.API.
type Client struct {
	registry *Registry
	keys     APIKeyResolver
}

// NewClient constructs a client backed by registry and an optional key resolver.
func NewClient(adapterRegistry *Registry, keys APIKeyResolver) (*Client, error) {
	if adapterRegistry == nil {
		return nil, errors.New("registry is required")
	}
	return &Client{registry: adapterRegistry, keys: keys}, nil
}

// Stream validates and starts an invocation. Errors before a stream exists are
// returned directly; failures after return terminate the stream with ErrorEvent.
func (llmClient *Client) Stream(
	ctx context.Context,
	targetModel Model,
	input Context,
	invocationOptions StreamOptions,
) (*EventStream, error) {
	if err := ValidateModel(targetModel); err != nil {
		return nil, err
	}
	if err := ValidateContext(input); err != nil {
		return nil, err
	}
	if err := ValidateOptions(invocationOptions); err != nil {
		return nil, err
	}
	protocolAdapter, err := llmClient.registry.resolve(targetModel.API)
	if err != nil {
		return nil, err
	}
	if protocolAdapter.API() != targetModel.API {
		return nil, ErrAPIMismatch
	}
	if invocationOptions.APIKey == "" && llmClient.keys != nil {
		if key, ok := llmClient.keys.APIKey(ctx, targetModel.Provider); ok {
			invocationOptions.APIKey = key
		}
	}
	return protocolAdapter.Stream(ctx, targetModel, input, invocationOptions)
}

// Complete waits for the terminal assistant message without draining events.
// Runtime failure is reported in the returned message's stop reason, while err
// is reserved for startup failure or cancellation of the waiting context.
func (llmClient *Client) Complete(
	ctx context.Context,
	targetModel Model,
	input Context,
	invocationOptions StreamOptions,
) (AssistantMessage, error) {
	responseStream, err := llmClient.Stream(ctx, targetModel, input, invocationOptions)
	if err != nil {
		return AssistantMessage{}, err
	}
	return responseStream.Result(ctx)
}
