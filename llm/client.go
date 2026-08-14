package llm

import (
	"context"
	"errors"
	"fmt"
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
	targetModel Model
	adapter     APIAdapter
	keys        APIKeyResolver
	tokens      TokenCounter
}

// ClientOption configures stable model-runtime behavior.
type ClientOption func(*clientConfig)

type clientConfig struct {
	tokens TokenCounter
}

// WithTokenCounter replaces the explicit conservative fallback with a
// model-aware tokenizer.
func WithTokenCounter(countStrategy TokenCounter) ClientOption {
	return func(configuration *clientConfig) {
		if countStrategy != nil {
			configuration.tokens = countStrategy
		}
	}
}

// NewClient validates targetModel, resolves its registered constructor, and creates a
// model-bound adapter for the lifetime of the client.
func NewClient(targetModel Model, adapterRegistry *Registry, keys APIKeyResolver, clientOptions ...ClientOption) (*Client, error) {
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
	configuration := clientConfig{tokens: ConservativeTokenCounter{}}
	for _, configure := range clientOptions {
		if configure != nil {
			configure(&configuration)
		}
	}
	return &Client{
		targetModel: targetSnapshot,
		adapter:     protocolAdapter,
		keys:        keys,
		tokens:      configuration.tokens,
	}, nil
}

// cloneModel gives Client an immutable snapshot independent of caller-owned
// slices and maps.
func cloneModel(targetModel Model) Model {
	cloned := targetModel
	cloned.Input = append([]InputModality(nil), targetModel.Input...)
	cloned.ReasoningLevels = append([]ReasoningLevel(nil), targetModel.ReasoningLevels...)
	if targetModel.ReasoningMap != nil {
		cloned.ReasoningMap = make(map[ReasoningLevel]string, len(targetModel.ReasoningMap))
		for level, mapped := range targetModel.ReasoningMap {
			cloned.ReasoningMap[level] = mapped
		}
	}
	if targetModel.ReasoningBudget != nil {
		cloned.ReasoningBudget = make(map[ReasoningLevel]int, len(targetModel.ReasoningBudget))
		for level, budget := range targetModel.ReasoningBudget {
			cloned.ReasoningBudget[level] = budget
		}
	}
	if targetModel.Headers != nil {
		cloned.Headers = make(map[string]string, len(targetModel.Headers))
		for name, value := range targetModel.Headers {
			cloned.Headers[name] = value
		}
	}
	if targetModel.ServiceTierCost != nil {
		cloned.ServiceTierCost = make(map[string]float64, len(targetModel.ServiceTierCost))
		for tier, multiplier := range targetModel.ServiceTierCost {
			cloned.ServiceTierCost[tier] = multiplier
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
	compiledSchemas, err := validateContext(input)
	if err != nil {
		return nil, err
	}
	resolvedOptions, err := ResolveStreamOptions(llmClient.targetModel, invocationOptions)
	if err != nil {
		return nil, err
	}
	invocationOptions = resolvedOptions
	if err := ValidateToolSelection(input.Tools, invocationOptions); err != nil {
		return nil, err
	}
	prepared, err := PrepareContext(llmClient.targetModel, input)
	if err != nil {
		return nil, err
	}
	attachToolValidators(prepared.Tools, compiledSchemas)
	if _, err := llmClient.validateBudget(ctx, prepared, invocationOptions); err != nil {
		return nil, err
	}
	if invocationOptions.APIKey == "" && llmClient.keys != nil {
		if key, ok := llmClient.keys.APIKey(ctx, llmClient.targetModel.Provider); ok {
			invocationOptions.APIKey = key
		}
	}
	return llmClient.adapter.Stream(ctx, prepared, invocationOptions)
}

// CountTokens prepares input for this Client's target model and returns the
// configured tokenizer result for observability and Context assembly.
func (llmClient *Client) CountTokens(ctx context.Context, input Context) (TokenCount, error) {
	if _, err := validateContext(input); err != nil {
		return TokenCount{}, err
	}
	prepared, err := PrepareContext(llmClient.targetModel, input)
	if err != nil {
		return TokenCount{}, err
	}
	return llmClient.countInputTokens(ctx, prepared)
}

func (llmClient *Client) countInputTokens(ctx context.Context, input Context) (TokenCount, error) {
	countedTokens, err := llmClient.tokens.CountTokens(ctx, llmClient.targetModel, input)
	if err != nil {
		return TokenCount{}, fmt.Errorf("count LLM context tokens: %w", err)
	}
	if err := validateTokenCount(countedTokens); err != nil {
		return TokenCount{}, err
	}
	return countedTokens, nil
}

func (llmClient *Client) validateBudget(ctx context.Context, input Context, invocationOptions StreamOptions) (TokenCount, error) {
	countedTokens, err := llmClient.countInputTokens(ctx, input)
	if err != nil {
		return TokenCount{}, err
	}
	reservedOutput := invocationOptions.MaxOutputTokens
	if llmClient.targetModel.ContextWindow > 0 && countedTokens.InputTokens+reservedOutput > llmClient.targetModel.ContextWindow {
		return countedTokens, fmt.Errorf(
			"%w: input=%d reserved_output=%d limit=%d strategy=%s estimated=%t",
			ErrContextWindowExceeded,
			countedTokens.InputTokens,
			reservedOutput,
			llmClient.targetModel.ContextWindow,
			countedTokens.Strategy,
			countedTokens.Estimated,
		)
	}
	return countedTokens, nil
}

func validateTokenCount(countedTokens TokenCount) error {
	if countedTokens.InputTokens < 0 {
		return errors.New("token counter returned a negative input count")
	}
	if countedTokens.Strategy == "" {
		return errors.New("token counter must identify its strategy")
	}
	return nil
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
