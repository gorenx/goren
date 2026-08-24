package tokenmeter

import (
	"encoding/json"
	"testing"

	"github.com/gorenx/goren/llm"
)

func TestEstimateMessageUsesSourceFixedHeuristic(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name    string
		content []llm.ContentBlock
		want    int64
	}{
		{
			name: "text",
			content: []llm.ContentBlock{
				llm.NewTextBlock("abcd"),
			},
			want: 9,
		},
		{
			name: "utf16-not-runes",
			content: []llm.ContentBlock{
				llm.NewTextBlock("😀😀😀😀"),
			},
			want: 10,
		},
		{
			name: "reasoning",
			content: []llm.ContentBlock{
				llm.ReasoningBlock{
					Text: "abcdefgh",
				},
			},
			want: 10,
		},
		{
			name: "tool-call",
			content: []llm.ContentBlock{
				llm.ToolCallBlock{
					ID:        "call-1",
					Name:      "bash",
					Arguments: `{"a":1}`,
				},
			},
			want: 11,
		},
		{
			name: "nested-tool-result",
			content: []llm.ContentBlock{
				llm.ToolResultBlock{
					ToolCallID: "call-1",
					Content: []llm.ContentBlock{
						llm.NewTextBlock("abcd"),
					},
				},
			},
			want: 13,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			messageValue := mustUserMessage(t, testCase.content)
			got, err := estimateMessage(messageValue)
			if err != nil {
				t.Fatal(err)
			}
			if got != testCase.want {
				t.Fatalf("estimate = %d, want %d", got, testCase.want)
			}
		})
	}
}

func TestEstimateMessagePricesOpaqueBlockFromLosslessJSON(t *testing.T) {
	t.Parallel()
	rawValue := json.RawMessage(`{"type":"mystery","payload":"abc"}`)
	block, err := llm.NewOpaqueContentBlock("mystery", rawValue)
	if err != nil {
		t.Fatal(err)
	}
	messageValue := mustUserMessage(t, []llm.ContentBlock{block})
	got, err := estimateMessage(messageValue)
	if err != nil {
		t.Fatal(err)
	}
	want := blockOverhead + estimateString(string(rawValue)) + roleOverhead
	if got != want {
		t.Fatalf("estimate = %d, want %d", got, want)
	}
}

func mustUserMessage(testingContext *testing.T, content []llm.ContentBlock) llm.UserMessage {
	testingContext.Helper()
	messageValue, err := llm.NewUserMessage(llm.UserMessageInput{
		Content: content,
		Source:  llm.UserMessageSource{},
	})
	if err != nil {
		testingContext.Fatal(err)
	}
	return messageValue
}
