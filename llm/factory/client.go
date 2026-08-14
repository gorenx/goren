package factory

import (
	"net/http"

	"github.com/gorenx/goren/llm"
	openaiadapter "github.com/gorenx/goren/llm/adapter/openai"
)

// Option configures dependencies owned by the LLM client factory.
type Option func(*clientConfig)

type clientConfig struct {
	httpClient    *http.Client
	keys          llm.APIKeyResolver
	clientOptions []llm.ClientOption
	openAIOptions []openaiadapter.AdapterOption
}

// WithHTTPClient provides the transport used by built-in adapters. A nil value
// keeps the adapter default.
func WithHTTPClient(transportClient *http.Client) Option {
	return func(configuration *clientConfig) {
		configuration.httpClient = transportClient
	}
}

// WithAPIKeyResolver provides credentials by serving provider.
func WithAPIKeyResolver(keyResolver llm.APIKeyResolver) Option {
	return func(configuration *clientConfig) {
		configuration.keys = keyResolver
	}
}

// WithClientOptions passes model-runtime options to the constructed Client.
func WithClientOptions(clientOptions ...llm.ClientOption) Option {
	return func(configuration *clientConfig) {
		configuration.clientOptions = append(configuration.clientOptions, clientOptions...)
	}
}

// WithOpenAIAdapterOptions configures the built-in OpenAI wire-protocol
// adapters. The options also apply to providers using a compatible wire
// protocol.
func WithOpenAIAdapterOptions(adapterOptions ...openaiadapter.AdapterOption) Option {
	return func(configuration *clientConfig) {
		configuration.openAIOptions = append(configuration.openAIOptions, adapterOptions...)
	}
}

// NewClient registers every built-in adapter, then creates a model-bound client
// by selecting the adapter identified by targetModel.API. Callers outside the
// LLM module do not need to assemble an adapter registry.
func NewClient(targetModel llm.Model, factoryOptions ...Option) (*llm.Client, error) {
	configuration := clientConfig{}
	for _, configure := range factoryOptions {
		if configure != nil {
			configure(&configuration)
		}
	}

	adapterRegistry := llm.NewRegistry()
	if err := registerBuiltInAdapters(adapterRegistry, configuration); err != nil {
		return nil, err
	}
	return llm.NewClient(targetModel, adapterRegistry, configuration.keys, configuration.clientOptions...)
}

func registerBuiltInAdapters(adapterRegistry *llm.Registry, configuration clientConfig) error {
	registrations := []struct {
		protocol  llm.API
		construct llm.AdapterConstructor
	}{
		{
			protocol: llm.APIOpenAICompletions,
			construct: func(configuredModel llm.Model) (llm.APIAdapter, error) {
				return openaiadapter.New(configuredModel, configuration.httpClient, configuration.openAIOptions...)
			},
		},
		{
			protocol: llm.APIOpenAIResponses,
			construct: func(configuredModel llm.Model) (llm.APIAdapter, error) {
				return openaiadapter.NewResponses(configuredModel, configuration.httpClient, configuration.openAIOptions...)
			},
		},
	}
	for _, registration := range registrations {
		if err := adapterRegistry.Register(registration.protocol, registration.construct); err != nil {
			return err
		}
	}
	return nil
}
