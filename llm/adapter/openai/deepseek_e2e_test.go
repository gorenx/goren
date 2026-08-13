package openai_test

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorenx/goren/llm"
	openaiadapter "github.com/gorenx/goren/llm/adapter/openai"
)

func TestDeepSeekStreamingE2E(t *testing.T) {
	if os.Getenv("GOREN_E2E_DEEPSEEK") != "1" {
		t.Skip("set GOREN_E2E_DEEPSEEK=1 to run the real DeepSeek test")
	}
	apiKey := os.Getenv("DEEPSEEK_API_KEY")
	if apiKey == "" {
		t.Fatal("DEEPSEEK_API_KEY is required")
	}
	baseURL := strings.TrimRight(os.Getenv("DEEPSEEK_API_BASE_URL"), "/")
	if baseURL == "" {
		baseURL = "https://api.deepseek.com"
	}
	modelID := os.Getenv("DEEPSEEK_MODEL")
	if modelID == "" {
		modelID = "deepseek-v4-flash"
	}

	targetModel := llm.Model{
		ID:              modelID,
		Name:            "DeepSeek E2E",
		API:             llm.APIOpenAICompletions,
		Provider:        "deepseek",
		BaseURL:         baseURL,
		Reasoning:       true,
		ReasoningLevels: []llm.ReasoningLevel{llm.ReasoningHigh, llm.ReasoningMax},
		Input:           []llm.InputModality{llm.InputText},
		ContextWindow:   1_000_000,
		MaxOutputTokens: 32,
	}
	adapterRegistry := llm.NewRegistry()
	err := adapterRegistry.Register(llm.APIOpenAICompletions, func(registeredModel llm.Model) (llm.APIAdapter, error) {
		return openaiadapter.New(
			registeredModel,
			http.DefaultClient,
			openaiadapter.WithCompatibility(openaiadapter.Compatibility{
				MaxTokensField:  openaiadapter.MaxTokensLegacy,
				ReasoningFormat: openaiadapter.ReasoningFormatDeepSeek,
			}),
		)
	})
	if err != nil {
		t.Fatal(err)
	}
	llmClient, err := llm.NewClient(targetModel, adapterRegistry, nil)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	responseStream, err := llmClient.Stream(
		ctx,
		llm.Context{Messages: []llm.Message{llm.NewTextMessage("Reply with exactly GOREN_E2E_OK and nothing else.")}},
		llm.StreamOptions{
			APIKey:          apiKey,
			Reasoning:       llm.ReasoningOff,
			Temperature:     floatPointer(0),
			MaxOutputTokens: 32,
			Timeout:         30 * time.Second,
			MaxRetries:      intPointer(1),
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	var sawStart, sawTextDelta bool
	for {
		streamedEvent, ok, err := responseStream.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		switch streamedEvent.(type) {
		case llm.StartEvent:
			sawStart = true
		case llm.TextDeltaEvent:
			sawTextDelta = true
		}
	}
	assistantReply, err := responseStream.Result(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if assistantReply.StopReason != llm.StopReasonStop {
		t.Fatalf("DeepSeek returned stop reason %q: %s", assistantReply.StopReason, assistantReply.ErrorMessage)
	}
	if strings.TrimSpace(llm.Text(assistantReply)) != "GOREN_E2E_OK" {
		t.Fatalf("unexpected DeepSeek text %q", llm.Text(assistantReply))
	}
	if !sawStart || !sawTextDelta {
		t.Fatalf("missing stream events: start=%t text_delta=%t", sawStart, sawTextDelta)
	}
	if assistantReply.ResponseID == "" || assistantReply.Usage.TotalTokens <= 0 {
		t.Fatalf("missing response identity or usage: %#v", assistantReply)
	}
}

func floatPointer(value float64) *float64 { return &value }
