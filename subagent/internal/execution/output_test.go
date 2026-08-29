package execution

import (
	"encoding/json"
	"testing"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
)

func TestSelectUsesLastNonEmptyAssistantMessage(t *testing.T) {
	t.Parallel()
	events := []session.Event{
		assistantMessageEvent(t, 1, agentmessage.NewTextBlock("step one")),
		assistantMessageEvent(t, 2, agentmessage.NewTextBlock("step two")),
		assistantMessageEvent(t, 3),
	}

	selected, selectErr := SelectAssistantOutput(events)
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
		assistantMessageEvent(t, 2, agentmessage.NewTextBlock("complete answer")),
		assistantChunkEvent(t, 3, llm.TextDeltaChunk{
			Index: 0,
			Text:  "later partial",
		}),
		assistantMessageEvent(t, 4),
	}

	selected, selectErr := SelectAssistantOutput(events)
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
		assistantMessageEvent(t, 2, agentmessage.ReasoningBlock{
			Type: "reasoning",
			Text: "complete reasoning",
		}),
	}

	selected, selectErr := SelectAssistantOutput(events)
	if selectErr != nil {
		t.Fatal(selectErr)
	}
	if len(selected) != 1 {
		t.Fatalf("selected = %#v", selected)
	}
	reasoning, matches := selected[0].(agentmessage.ReasoningBlock)
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

	selected, selectErr := SelectAssistantOutput(events)
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

	selected, selectErr := SelectAssistantOutput(events)
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
	content ...agentmessage.ContentBlock,
) session.Event {
	t.Helper()
	messageValue, messageErr := agentmessage.NewAssistantMessage(
		agentmessage.AssistantMessageInput{
			Content: content,
			Source: agentmessage.ModelMessageSource{
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
	content []agentmessage.ContentBlock,
	want string,
) {
	t.Helper()
	if len(content) != 1 {
		t.Fatalf("content = %#v", content)
	}
	textBlock, matches := content[0].(agentmessage.TextBlock)
	if !matches || textBlock.Text != want {
		t.Fatalf("content = %#v", content)
	}
}
