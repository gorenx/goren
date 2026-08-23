//go:build contract

package contract_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/llm/retry"
)

type retryContractAdapter struct {
	mu           sync.Mutex
	requestCount int
	policy       llm.NormalRetryPolicy
}

func (backend *retryContractAdapter) ProviderRetryPolicy(string) (llm.RetryPolicy, error) {
	return backend.policy.CloneRetryPolicy(), nil
}

func (backend *retryContractAdapter) Stream(
	_ context.Context,
	_ llm.GenerateOptions,
) (llm.ChunkStream, error) {
	backend.mu.Lock()
	backend.requestCount++
	requestNumber := backend.requestCount
	backend.mu.Unlock()
	switch requestNumber {
	case 1:
		statusCode := 429
		return nil, llm.MustLlmError("busy", "RATE_LIMIT", llm.LlmErrorOptions{
			Status: &statusCode,
		})
	case 2:
		retryAfter := 3.0
		return nil, llm.MustLlmError("retry later", "SERVER", llm.LlmErrorOptions{
			ProviderRetryAfterMS: &retryAfter,
		})
	case 3:
		return llm.NewSliceStream([]llm.StreamChunk{
			llm.BlockEndChunk{
				Index: 0,
				Block: llm.NewTextBlock("recovered"),
			},
			llm.FinishChunk{
				Reason: llm.StopFinish{},
			},
		})
	default:
		return nil, errors.New("retry contract adapter script exhausted")
	}
}

func (backend *retryContractAdapter) requests() int {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	return backend.requestCount
}

type retryContractEvent struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

type retryContractObservation struct {
	RetryEvents  []retryContractEvent `json:"retryEvents"`
	RequestCount int                  `json:"requestCount"`
	Roles        []llm.MessageRole    `json:"roles"`
	FinalText    *string              `json:"finalText"`
}

func TestPinnedSourceLLMRetryMatchesGo(t *testing.T) {
	repositoryRoot, sourceRoot := contractPaths(t)
	commandContext, cancelCommand := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelCommand()
	sourceOutput, err := runTypeScript(commandContext, sourceRoot,
		filepath.Join(repositoryRoot, "tests", "contract", "typescript", "llm-retry.ts"),
		sourceRoot, filepath.Join(repositoryRoot, "contracts", "deepseek-harness", "manifest.json"),
	)
	if err != nil {
		t.Fatal(err)
	}

	contractState := &agentLoopContractState{}
	retryPlugin := llmretry.New(llmretry.RuntimeOptions{
		Random: func() float64 {
			return 0
		},
		NewRetryID: func() (llmretry.RetryID, error) {
			return "chain-1", nil
		},
	})
	if err = startAgentLoopContractState(t, contractState, retryPlugin); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if shutdownErr := contractState.engine.Shutdown(context.Background()); shutdownErr != nil {
			t.Error(shutdownErr)
		}
	})
	backend := &retryContractAdapter{
		policy: llm.NormalRetryPolicy{
			ResolvedRetryBackoff: llm.ResolvedRetryBackoff{
				InitialDelayMS: 1,
				MaxDelayMS:     4,
				JitterRatio:    0.5,
			},
			Mode:           llm.RetryNormal,
			MaxRetries:     2,
			RetryableCodes: []string{"SERVER", "RATE_LIMIT"},
		},
	}
	if _, err = contractState.models.RegisterAdapter(
		context.Background(),
		[]string{
			"mock",
		},
		backend,
	); err != nil {
		t.Fatal(err)
	}
	handle, err := contractState.agents.Create(
		context.Background(),
		agent.CreateOptions{
			SessionID: "retry-contract",
			AgentOptions: agent.Options{
				Provider: "mock",
				Model:    "model",
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if disposeErr := handle.Dispose(context.Background()); disposeErr != nil {
			t.Error(disposeErr)
		}
	}()
	messageValue, err := llm.NewUserMessage(llm.UserMessageInput{
		Content: []llm.ContentBlock{llm.NewTextBlock("recover")},
		Source:  llm.UserMessageSource{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := handle.Subject.Followup(messageValue); err != nil {
		t.Fatal(err)
	}
	waitContext, cancelWait := context.WithTimeout(context.Background(), time.Second)
	defer cancelWait()
	if err := handle.Subject.WhenIdle(waitContext); err != nil {
		t.Fatal(err)
	}

	conversation := handle.Subject.SessionValue()
	retryEvents := make([]retryContractEvent, 0, 4)
	for _, committed := range conversation.Events() {
		if committed.Type == llmretry.RetryEventName || committed.Type == llmretry.RetryStartedEventName {
			retryEvents = append(retryEvents, retryContractEvent{
				Type: committed.Type,
				Data: append(json.RawMessage(nil), committed.Data...),
			})
		}
	}
	messages, err := conversation.DeriveMessages()
	if err != nil {
		t.Fatal(err)
	}
	roles := make([]llm.MessageRole, 0, len(messages))
	for _, entry := range messages {
		roles = append(roles, entry.ConversationRole())
	}
	var finalText *string
	if len(messages) != 0 {
		blocks := messages[len(messages)-1].ContentValue()
		if len(blocks) != 0 {
			if textBlock, ok := blocks[0].(llm.TextBlock); ok {
				textValue := textBlock.Text
				finalText = &textValue
			}
		}
	}
	goOutput, err := json.Marshal(retryContractObservation{
		RetryEvents:  retryEvents,
		RequestCount: backend.requests(),
		Roles:        roles,
		FinalText:    finalText,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, goOutput, sourceOutput)
}
