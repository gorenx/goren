package factory_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/gorenx/goren/llm"
	openaiadapter "github.com/gorenx/goren/llm/adapter/openai"
	llmfactory "github.com/gorenx/goren/llm/factory"
)

func TestNewClientRegistersBuiltInAdapters(t *testing.T) {
	tests := []struct {
		name     string
		protocol llm.API
	}{
		{name: "chat completions", protocol: llm.APIOpenAICompletions},
		{name: "responses", protocol: llm.APIOpenAIResponses},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			llmClient, err := llmfactory.NewClient(validModel(testCase.protocol))
			if err != nil {
				t.Fatalf("create client: %v", err)
			}
			if llmClient == nil {
				t.Fatal("client is nil")
			}
		})
	}
}

func TestNewClientRejectsUnregisteredProtocol(t *testing.T) {
	_, err := llmfactory.NewClient(validModel("custom-protocol"))
	if !errors.Is(err, llm.ErrAdapterNotRegistered) {
		t.Fatalf("got %v, want ErrAdapterNotRegistered", err)
	}
}

func TestNewClientAppliesFactoryOptions(t *testing.T) {
	countStrategy := llm.TokenCounterFunc(func(context.Context, llm.Model, llm.Context) (llm.TokenCount, error) {
		return llm.TokenCount{InputTokens: 7, Strategy: "factory-test"}, nil
	})
	llmClient, err := llmfactory.NewClient(
		validModel(llm.APIOpenAICompletions),
		llmfactory.WithClientOptions(llm.WithTokenCounter(countStrategy)),
	)
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	countedTokens, err := llmClient.CountTokens(
		context.Background(),
		llm.Context{Messages: []llm.Message{llm.NewTextMessage("hello")}},
	)
	if err != nil {
		t.Fatalf("count tokens: %v", err)
	}
	if countedTokens.InputTokens != 7 || countedTokens.Strategy != "factory-test" {
		t.Fatalf("unexpected token count %#v", countedTokens)
	}
}

func ExampleNewClient() {
	llmClient, err := llmfactory.NewClient(validModel(llm.APIOpenAIResponses))
	fmt.Println(llmClient != nil, err)
	// Output: true <nil>
}

func TestNewClientPassesOpenAIAdapterOptions(t *testing.T) {
	_, err := llmfactory.NewClient(
		validModel(llm.APIOpenAICompletions),
		llmfactory.WithOpenAIAdapterOptions(openaiadapter.WithCompatibility(openaiadapter.Compatibility{
			SystemRole: "unsupported-role",
		})),
	)
	if err == nil {
		t.Fatal("create client accepted invalid OpenAI compatibility")
	}
}

func validModel(protocol llm.API) llm.Model {
	return llm.Model{
		ID:              "test-model",
		Name:            "Test Model",
		API:             protocol,
		Provider:        "test-provider",
		BaseURL:         "https://example.com/v1",
		Input:           []llm.InputModality{llm.InputText},
		ContextWindow:   4_096,
		MaxOutputTokens: 1_024,
	}
}
