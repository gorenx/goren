package tokenmeter

import (
	"encoding/json"
	"testing"

	"github.com/gorenx/goren/agentmessage"
)

func TestEstimateMessageUsesSourceFixedHeuristic(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name    string
		content []agentmessage.ContentBlock
		want    int64
	}{
		{
			name: "text",
			content: []agentmessage.ContentBlock{
				agentmessage.NewTextBlock("abcd"),
			},
			want: 9,
		},
		{
			name: "utf16-not-runes",
			content: []agentmessage.ContentBlock{
				agentmessage.NewTextBlock("😀😀😀😀"),
			},
			want: 10,
		},
		{
			name: "reasoning",
			content: []agentmessage.ContentBlock{
				agentmessage.ReasoningBlock{
					Text: "abcdefgh",
				},
			},
			want: 10,
		},
		{
			name: "tool-call",
			content: []agentmessage.ContentBlock{
				agentmessage.ToolCallBlock{
					ID:        "call-1",
					Name:      "bash",
					Arguments: `{"a":1}`,
				},
			},
			want: 11,
		},
		{
			name: "nested-tool-result",
			content: []agentmessage.ContentBlock{
				agentmessage.ToolResultBlock{
					ToolCallID: "call-1",
					Content: []agentmessage.ContentBlock{
						agentmessage.NewTextBlock("abcd"),
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
	block, err := agentmessage.NewOpaqueContentBlock("mystery", rawValue)
	if err != nil {
		t.Fatal(err)
	}
	messageValue := mustUserMessage(t, []agentmessage.ContentBlock{block})
	got, err := estimateMessage(messageValue)
	if err != nil {
		t.Fatal(err)
	}
	want := blockOverhead + estimateString(string(rawValue)) + roleOverhead
	if got != want {
		t.Fatalf("estimate = %d, want %d", got, want)
	}
}

func mustUserMessage(testingContext *testing.T, content []agentmessage.ContentBlock) agentmessage.UserMessage {
	testingContext.Helper()
	messageValue, err := agentmessage.NewUserMessage(agentmessage.UserMessageInput{
		Content: content,
		Source:  agentmessage.UserMessageSource{},
	})
	if err != nil {
		testingContext.Fatal(err)
	}
	return messageValue
}
