package assistantoutput

import (
	"encoding/json"
	"testing"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
)

func TestSelectUsesLastNonEmptyAssistantMessage(t *testing.T) {
	t.Parallel()
	events := []session.Event{
		assistantMessageEvent(t, 1, llm.NewTextBlock("step one")),
		assistantMessageEvent(t, 2, llm.NewTextBlock("step two")),
		assistantMessageEvent(t, 3),
	}

	selected, selectErr := Select(events)
	if selectErr != nil {
		t.Fatal(selectErr)
	}
	assertOutput(t, selected, "step two")
}

func TestSelectPrefersMessageOverAllStreamedText(t *testing.T) {
	t.Parallel()
	events := []session.Event{
		assistantChunkEvent(t, 1, llm.TextDeltaChunk{
			Index: 0,
			Text:  "earlier partial",
		}),
		assistantMessageEvent(t, 2, llm.NewTextBlock("complete answer")),
		assistantChunkEvent(t, 3, llm.TextDeltaChunk{
			Index: 0,
			Text:  "later partial",
		}),
		assistantMessageEvent(t, 4),
	}

	selected, selectErr := Select(events)
	if selectErr != nil {
		t.Fatal(selectErr)
	}
	assertOutput(t, selected, "complete answer")
}

func TestSelectTreatsReasoningAsNonEmptyMessage(t *testing.T) {
	t.Parallel()
	events := []session.Event{
		assistantChunkEvent(t, 1, llm.TextDeltaChunk{
			Index: 0,
			Text:  "streamed text",
		}),
		assistantMessageEvent(t, 2, llm.ReasoningBlock{
			Type: "reasoning",
			Text: "complete reasoning",
		}),
	}

	selected, selectErr := Select(events)
	if selectErr != nil {
		t.Fatal(selectErr)
	}
	if len(selected) != 1 {
		t.Fatalf("selected = %#v", selected)
	}
	reasoning, matches := selected[0].(llm.ReasoningBlock)
	if !matches || reasoning.Text != "complete reasoning" {
		t.Fatalf("selected = %#v", selected)
	}
}

func TestSelectFallsBackToTextDeltasOnly(t *testing.T) {
	t.Parallel()
	events := []session.Event{
		assistantChunkEvent(t, 1, llm.ReasoningDeltaChunk{
			Index: 0,
			Text:  "thinking",
		}),
		assistantChunkEvent(t, 2, llm.TextDeltaChunk{
			Index: 0,
			Text:  "partial ",
		}),
		{
			Type: session.ToolResultEventName,
			Seq:  3,
			Data: json.RawMessage(`{}`),
		},
		assistantChunkEvent(t, 4, llm.TextDeltaChunk{
			Index: 0,
			Text:  "answer",
		}),
		assistantMessageEvent(t, 5),
	}

	selected, selectErr := Select(events)
	if selectErr != nil {
		t.Fatal(selectErr)
	}
	assertOutput(t, selected, "partial answer")
}

func TestSelectReturnsNoOutputWithoutMessageOrText(t *testing.T) {
	t.Parallel()
	events := []session.Event{
		assistantChunkEvent(t, 1, llm.ReasoningDeltaChunk{
			Index: 0,
			Text:  "thinking",
		}),
		assistantMessageEvent(t, 2),
	}

	selected, selectErr := Select(events)
	if selectErr != nil {
		t.Fatal(selectErr)
	}
	if selected != nil {
		t.Fatalf("selected = %#v", selected)
	}
}

func assistantMessageEvent(
	t *testing.T,
	sequence int64,
	content ...llm.ContentBlock,
) session.Event {
	t.Helper()
	messageValue, messageErr := llm.NewAssistantMessage(
		llm.AssistantMessageInput{
			Content: content,
			Source: llm.ModelMessageSource{
				Provider: "mock",
				Model:    "model",
			},
		},
	)
	if messageErr != nil {
		t.Fatal(messageErr)
	}
	return serializedEvent(
		t,
		sequence,
		session.AssistantMessageEventName,
		session.AssistantMessage{
			Turn:    1,
			Step:    1,
			Message: messageValue,
		},
	)
}

func assistantChunkEvent(
	t *testing.T,
	sequence int64,
	chunkValue llm.StreamChunk,
) session.Event {
	t.Helper()
	return serializedEvent(
		t,
		sequence,
		session.AssistantChunkEventName,
		session.AssistantChunk{
			Turn:  1,
			Step:  1,
			Chunk: chunkValue,
		},
	)
}

func serializedEvent(
	t *testing.T,
	sequence int64,
	eventType string,
	payload any,
) session.Event {
	t.Helper()
	rawValue, encodeErr := json.Marshal(payload)
	if encodeErr != nil {
		t.Fatal(encodeErr)
	}
	return session.Event{
		Type: eventType,
		Seq:  sequence,
		Data: rawValue,
	}
}

func assertOutput(
	t *testing.T,
	content []llm.ContentBlock,
	want string,
) {
	t.Helper()
	if len(content) != 1 {
		t.Fatalf("content = %#v", content)
	}
	textBlock, matches := content[0].(llm.TextBlock)
	if !matches || textBlock.Text != want {
		t.Fatalf("content = %#v", content)
	}
}
