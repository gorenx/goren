package basic

import (
	"context"
	"strings"
	"testing"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/compaction"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
)

func TestLLMSummarizerRejectsUnsafeOrIncompleteOutput(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name      string
		chunks    []llm.StreamChunk
		nilStream bool
		wantError string
	}{
		{
			name: "max tokens",
			chunks: []llm.StreamChunk{
				llm.TextDeltaChunk{
					Index: 0,
					Text:  "truncated",
				},
				llm.FinishChunk{
					Reason: llm.MaxTokensFinish{},
				},
			},
			wantError: "truncated at the token cap",
		},
		{
			name: "provider error",
			chunks: []llm.StreamChunk{
				llm.FinishChunk{
					Reason: llm.ErrorFinish{
						Failure: llm.LlmFailure{
							Message: "provider unavailable",
							Code:    "SERVER",
						},
					},
				},
			},
			wantError: "provider unavailable",
		},
		{
			name: "provider aborted",
			chunks: []llm.StreamChunk{
				llm.FinishChunk{
					Reason: llm.AbortedFinish{
						Failure: llm.LlmFailure{
							Message: "provider aborted summary",
							Code:    "ABORTED",
						},
					},
				},
			},
			wantError: "provider aborted summary",
		},
		{
			name: "image",
			chunks: []llm.StreamChunk{
				llm.BlockEndChunk{
					Index: 0,
					Block: agentmessage.ImageBlock{},
				},
				llm.FinishChunk{
					Reason: llm.StopFinish{},
				},
			},
			wantError: "cannot contain image",
		},
		{
			name: "reasoning only",
			chunks: []llm.StreamChunk{
				llm.ReasoningDeltaChunk{
					Index: 0,
					Text:  "private only",
				},
				llm.FinishChunk{
					Reason: llm.StopFinish{},
				},
			},
			wantError: "no text summary content",
		},
		{
			name: "empty text",
			chunks: []llm.StreamChunk{
				llm.TextDeltaChunk{
					Index: 0,
					Text:  "  \n\t",
				},
				llm.FinishChunk{
					Reason: llm.StopFinish{},
				},
			},
			wantError: "no text summary content",
		},
		{
			name:      "nil stream",
			nilStream: true,
			wantError: "nil summary stream",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			conversation, err := session.New(
				session.SessionID("summarizer-"+testCase.name),
				session.CreateOptions{},
			)
			if err != nil {
				t.Fatal(err)
			}
			runtimeValue := &runtimeStub{
				chunks:    testCase.chunks,
				nilStream: testCase.nilStream,
			}
			summarizer := newLLMSummarizer(newPolicyCatalog(ResolvedConfig{
				SummarizationProvider: fixtureProvider,
				SummarizationModel:    fixtureModel,
				MaxTokens:             100,
			}))
			summarizer.bind(runtimeValue)
			t.Cleanup(summarizer.release)
			_, err = summarizer.summarize(
				context.Background(),
				summarizationInput{},
				compaction.AgentContext{
					Session: conversation,
				},
			)
			if err == nil || !strings.Contains(err.Error(), testCase.wantError) {
				t.Fatalf("summarize error = %v, want %q", err, testCase.wantError)
			}
		})
	}
}

func TestLLMSummarizerUsesExactRouteOverride(t *testing.T) {
	t.Parallel()
	provider := "summary-provider"
	model := "summary-model"
	maximum := 321
	resolved, err := ResolveConfig(Config{
		ModelPolicies: []ModelPolicyConfig{
			{
				Provider: fixtureProvider,
				Model:    fixtureModel,
				PolicyConfig: PolicyConfig{
					SummarizationProvider: &provider,
					SummarizationModel:    &model,
					MaxTokens:             &maximum,
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	conversation := conversationFixture(t, 1, "route")
	runtimeValue := newRuntimeStub("checkpoint", 1_000)
	summarizer := newLLMSummarizer(newPolicyCatalog(resolved))
	summarizer.bind(runtimeValue)
	t.Cleanup(summarizer.release)
	result, err := summarizer.summarize(
		context.Background(),
		summarizationInput{},
		compaction.AgentContext{
			Session: conversation,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.provider != provider || result.model != model ||
		result.maxTokens == nil || *result.maxTokens != maximum {
		t.Fatalf("summary result = %#v", result)
	}
	requests := runtimeValue.requestValues()
	if len(requests) != 1 || requests[0].Provider != provider ||
		requests[0].Model != model || requests[0].MaxTokens == nil ||
		*requests[0].MaxTokens != maximum {
		t.Fatalf("summary requests = %#v", requests)
	}
}

func TestCompactRegionRejectsNonShrinkingCheckpoint(t *testing.T) {
	t.Parallel()
	conversation := conversationFixture(t, 2, "history")
	meterValue := &meterStub{
		estimate: 200,
	}
	implementation := newBoundCompaction(
		t,
		Config{
			Auto: boolPointer(false),
		},
		newRuntimeStub("large checkpoint", 1_000),
		meterValue,
		&liveStoreStub{},
		nil,
	)
	before := conversation.Surface()
	_, err := implementation.CompactRegion(
		context.Background(),
		before.Nodes[0],
		before.Nodes[1],
		compaction.AgentContext{
			Session: conversation,
		},
	)
	if err == nil || !strings.Contains(err.Error(), "summary is not smaller") {
		t.Fatalf("CompactRegion error = %v", err)
	}
	state, inspectErr := compaction.InspectLog(conversation.Events())
	if inspectErr != nil || state.Attempt != nil {
		t.Fatalf("non-shrinking state = %#v, error = %v", state, inspectErr)
	}
}
