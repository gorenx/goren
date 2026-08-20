//go:build contract

package contract_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorenx/goren/internal/llm/deepseek"
	"github.com/gorenx/goren/llm"
)

type llmAssemblyObservation struct {
	Blocks []llm.ContentBlock `json:"blocks"`
	Usage  llm.TokenUsage     `json:"usage"`
	Finish llm.FinishReason   `json:"finish"`
}

type llmContractObservation struct {
	WireMessages any                    `json:"wireMessages"`
	WireRequest  any                    `json:"wireRequest"`
	Chunks       []llm.StreamChunk      `json:"chunks"`
	Assembled    llmAssemblyObservation `json:"assembled"`
	RetryDefault llm.RetryPolicy        `json:"retryDefault"`
}

type contractConnectionSource struct {
	configuration deepseek.ConnectionOptions
}

func (source contractConnectionSource) CurrentConnection() (deepseek.ConnectionOptions, error) {
	return source.configuration.Snapshot(), nil
}

type contractAPIKeyResolver struct{}

func (contractAPIKeyResolver) ResolveAPIKey(
	context.Context,
	deepseek.ConnectionOptions,
) (string, error) {
	return "key", nil
}

type contractUserIDProvider struct{}

func (contractUserIDProvider) UserID() (string, error) {
	return "00000000-0000-4000-8000-000000000001", nil
}

func TestPinnedSourceLLMDeepSeekMatchesGo(t *testing.T) {
	repositoryRoot, sourceRoot := contractPaths(t)
	commandContext, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sourceOutput, err := runTypeScript(commandContext, sourceRoot,
		filepath.Join(repositoryRoot, "tests", "contract", "typescript", "llm-deepseek.ts"),
		sourceRoot, filepath.Join(repositoryRoot, "contracts", "deepseek-harness", "manifest.json"),
	)
	if err != nil {
		t.Fatal(err)
	}

	conversation := []llm.Message{
		contractLLMMessage(t, llm.RoleAssistant, []llm.ContentBlock{
			llm.ReasoningBlock{Type: "reasoning", Text: "think"},
			llm.ToolCallBlock{Type: "tool-call", ID: "call-1", Name: "lookup", Arguments: `{"q":"x"}`},
		}),
		contractLLMMessage(t, llm.RoleUser, []llm.ContentBlock{
			llm.TextBlock{Type: "text", Text: "note"},
			llm.ToolResultBlock{Type: "tool-result", ToolCallID: "call-1", Content: []llm.ContentBlock{}},
		}),
	}
	wireMessages, err := deepseek.SerializeMessages(conversation)
	if err != nil {
		t.Fatal(err)
	}
	emptyStop := []string{}
	requestOptions := llm.GenerateOptions{
		CallConfig: llm.CallConfig{
			Provider: deepseek.ProviderRoute, Model: "deepseek-v4-flash",
			ReasoningEffort: "max", Stop: emptyStop,
		},
		Messages: conversation, System: contractString("system"),
		Tools: []llm.ToolSchema{{Name: "lookup", Description: "Lookup", Parameters: json.RawMessage(`{"type":"object"}`)}},
	}
	high := deepseek.ReasoningHigh
	enabled := deepseek.ThinkingEnabled
	wireRequest, err := deepseek.SerializeRequest(requestOptions, deepseek.RequestDefaults{
		Thinking: &enabled, ReasoningEffort: &high,
	})
	if err != nil {
		t.Fatal(err)
	}

	testServer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.Header().Set("content-type", "text/event-stream")
		_, _ = responseWriter.Write([]byte(
			"data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"\"}}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"think\"}}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{\"content\":\"answer\"}}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call-2\",\"function\":{\"name\":\"lookup\",\"arguments\":\"{}\"}}]}}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":4,\"prompt_cache_hit_tokens\":7}}\n\n" +
				"data: [DONE]\n\n",
		))
	}))
	defer testServer.Close()
	baseURL := testServer.URL
	connection, err := deepseek.ResolveOptions(deepseek.Config{
		BaseURL: &baseURL,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	backend, err := deepseek.NewAdapter(deepseek.AdapterDependencies{
		Connections: contractConnectionSource{
			configuration: connection,
		},
		Credentials: contractAPIKeyResolver{},
		Identity:    contractUserIDProvider{},
	})
	if err != nil {
		t.Fatal(err)
	}
	chunkFlow, err := backend.Stream(context.Background(), llm.GenerateOptions{CallConfig: llm.CallConfig{Model: "m"}})
	if err != nil {
		t.Fatal(err)
	}
	chunks := make([]llm.StreamChunk, 0)
	assembler := llm.NewBlockAssembler()
	for {
		entry, available, nextErr := chunkFlow.Next(context.Background())
		if nextErr != nil {
			t.Fatal(nextErr)
		}
		if !available {
			break
		}
		chunks = append(chunks, entry)
		if err := assembler.Push(entry); err != nil {
			t.Fatal(err)
		}
	}
	blocks, err := assembler.AssembledBlocks()
	if err != nil {
		t.Fatal(err)
	}
	usage, found := assembler.UsageValue()
	if !found {
		t.Fatal("translated stream omitted usage")
	}
	retryPolicy, err := llm.ResolveRetryPolicy(nil, "contract")
	if err != nil {
		t.Fatal(err)
	}
	goOutput, err := json.Marshal(llmContractObservation{
		WireMessages: wireMessages, WireRequest: wireRequest, Chunks: chunks,
		Assembled:    llmAssemblyObservation{Blocks: blocks, Usage: usage, Finish: assembler.FinishValue()},
		RetryDefault: retryPolicy,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, goOutput, sourceOutput)
}

func contractLLMMessage(t *testing.T, role llm.MessageRole, content []llm.ContentBlock) llm.Message {
	t.Helper()
	entry, err := llm.NewMessage(llm.MessageInput{
		Role: role, Content: content, Source: llm.PluginMessageSource{Plugin: "contract"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return entry
}

func contractString(value string) *string { return &value }
