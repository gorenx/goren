package basic

import (
	"strings"
	"testing"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/llm"
)

func TestLLMSummarizerRejectsUnsafeOrIncompleteOutput(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name      string
		chunks    []llm.StreamChunk
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
			name:      "empty output",
			wantError: "no text summary content",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			summarizer := NewSummarizer(ResolvedConfig{})
			for _, chunk := range testCase.chunks {
				if err := summarizer.Push(chunk); err != nil {
					t.Fatal(err)
				}
			}
			_, err := summarizer.Result()
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
				Provider: "conversation",
				Model:    "main",
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
	snapshot, _ := selectionSnapshot(t, 2)
	snapshot.Barrier.SessionID = "agent"
	summarizer := NewSummarizer(resolved)
	request, err := summarizer.Request(snapshot, []int64{0}, RouteTarget{
		Provider: "conversation",
		Model:    "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	if request.Provider != provider || request.Model != model || request.MaxTokens == nil || *request.MaxTokens != maximum {
		t.Fatalf("summary request = %#v", request)
	}
	if request.Purpose != llm.PurposeCompaction || request.SessionID != "agent" || len(request.Messages) != 2 {
		t.Fatalf("summary input = %#v", request)
	}
	text, ok := request.Messages[1].ContentValue()[0].(agentmessage.TextBlock)
	if !ok || text.Text != compactionInstruction {
		t.Fatal("summary instruction changed")
	}
	// A caller changing its request must not mutate the captured policy.
	*request.MaxTokens = 1
	repeated, err := summarizer.Request(snapshot, []int64{0}, RouteTarget{
		Provider: "conversation",
		Model:    "main",
	})
	if err != nil || repeated.MaxTokens == nil || *repeated.MaxTokens != maximum {
		t.Fatalf("request shares policy: %v", err)
	}
}

func TestSummaryRetainsRawOutputAndDetachesText(t *testing.T) {
	summarizer := NewSummarizer(ResolvedConfig{})
	for _, chunk := range []llm.StreamChunk{
		llm.ReasoningDeltaChunk{
			Index: 0,
			Text:  "private reasoning",
		},
		llm.TextDeltaChunk{
			Index: 1,
			Text:  "checkpoint",
		},
		llm.FinishChunk{
			Reason: llm.StopFinish{},
		},
	} {
		if err := summarizer.Push(chunk); err != nil {
			t.Fatal(err)
		}
	}
	result, err := summarizer.Result()
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Content) != 1 || len(result.Output) != 2 {
		t.Fatalf("summary = %#v", result)
	}
	result.Content[0] = agentmessage.NewTextBlock("external change")
	again, err := summarizer.Result()
	if err != nil {
		t.Fatal(err)
	}
	if again.Content[0].(agentmessage.TextBlock).Text != "checkpoint" {
		t.Fatal("summary aliases returned content")
	}
}
