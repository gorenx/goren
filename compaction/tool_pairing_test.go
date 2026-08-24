package compaction

import (
	"testing"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
)

func TestToolPairingBalanceUsesCurrentSurfacePositions(t *testing.T) {
	t.Parallel()
	conversation := newCompactionSession(t, "tool-pairing")
	beforeSeq := appendCompactionUser(t, conversation, "before")
	assistantValue, err := llm.NewAssistantMessage(llm.AssistantMessageInput{
		Content: []llm.ContentBlock{
			llm.ToolCallBlock{
				ID:        "call-1",
				Name:      "read",
				Arguments: `{}`,
			},
		},
		Source: llm.ModelMessageSource{
			Provider: "mock",
			Model:    "model-a",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	emptySources := []int64{}
	assistantEntry, err := session.AppendSurface(
		conversation,
		session.AssistantMessaged,
		session.AssistantMessage{
			Turn:    1,
			Step:    1,
			Message: assistantValue,
		},
		session.SurfaceIntent{
			Operation:       session.SurfaceAppend(),
			SourceEventSeqs: &emptySources,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	resultValue, err := llm.NewToolResultMessage(llm.ToolResultMessageInput{
		CallID: "call-1",
		Content: []llm.ContentBlock{
			llm.NewTextBlock("result"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	resultEntry, err := session.AppendSurface(
		conversation,
		session.ToolResultAdded,
		session.ToolResult{
			Turn:    1,
			Step:    1,
			Message: resultValue,
		},
		session.SurfaceIntent{
			Operation: session.SurfaceAppend(),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	afterSeq := appendCompactionUser(t, conversation, "after")

	assertToolPairingCut(t, conversation, beforeSeq, true, true)
	assertToolPairingCut(t, conversation, assistantEntry.Seq, true, false)
	assertToolPairingCut(t, conversation, resultEntry.Seq, false, true)
	assertToolPairingCut(t, conversation, afterSeq, true, true)

	replacement := mustCompactionUserMessage(t, "summary")
	replacedSeqs := []int64{beforeSeq}
	replacementEntry, err := session.AppendSurface(
		conversation,
		session.UserMessageAdded,
		replacement,
		session.SurfaceIntent{
			Operation:       session.SurfaceReplace(beforeSeq, beforeSeq),
			SourceEventSeqs: &replacedSeqs,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	assertToolPairingCut(t, conversation, replacementEntry.Seq, true, true)
	assertToolPairingCut(t, conversation, assistantEntry.Seq, true, false)
}

func TestToolPairingBalanceRejectsOrphanResultAndAbsentSequence(t *testing.T) {
	t.Parallel()
	conversation := newCompactionSession(t, "tool-orphan")
	resultValue, err := llm.NewToolResultMessage(llm.ToolResultMessageInput{
		CallID:  "orphan",
		Content: []llm.ContentBlock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	resultEntry, err := session.AppendSurface(
		conversation,
		session.ToolResultAdded,
		session.ToolResult{
			Turn:    1,
			Step:    1,
			Message: resultValue,
		},
		session.SurfaceIntent{
			Operation: session.SurfaceAppend(),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ToolPairingBalancedAfter(conversation, resultEntry.Seq); err == nil {
		t.Fatal("orphan tool/result was accepted")
	}

	clean := newCompactionSession(t, "tool-absent")
	currentSeq := appendCompactionUser(t, clean, "current")
	if _, err := ToolPairingBalancedBefore(clean, currentSeq+1); err == nil {
		t.Fatal("absent Surface sequence was accepted")
	}
}

func assertToolPairingCut(
	testingContext *testing.T,
	conversation *session.Session,
	sequence int64,
	wantBefore bool,
	wantAfter bool,
) {
	testingContext.Helper()
	before, err := ToolPairingBalancedBefore(conversation, sequence)
	if err != nil {
		testingContext.Fatal(err)
	}
	after, err := ToolPairingBalancedAfter(conversation, sequence)
	if err != nil {
		testingContext.Fatal(err)
	}
	if before != wantBefore || after != wantAfter {
		testingContext.Fatalf(
			"seq %d cuts = (%t, %t), want (%t, %t)",
			sequence,
			before,
			after,
			wantBefore,
			wantAfter,
		)
	}
}

func newCompactionSession(
	testingContext *testing.T,
	identifier session.SessionID,
) *session.Session {
	testingContext.Helper()
	conversation, err := session.New(identifier, session.CreateOptions{})
	if err != nil {
		testingContext.Fatal(err)
	}
	return conversation
}

func appendCompactionUser(
	testingContext *testing.T,
	conversation *session.Session,
	textValue string,
) int64 {
	testingContext.Helper()
	messageValue := mustCompactionUserMessage(testingContext, textValue)
	committed, err := session.AppendSurface(
		conversation,
		session.UserMessageAdded,
		messageValue,
		session.SurfaceIntent{
			Operation: session.SurfaceAppend(),
		},
	)
	if err != nil {
		testingContext.Fatal(err)
	}
	return committed.Seq
}

func mustCompactionUserMessage(
	testingContext *testing.T,
	textValue string,
) llm.UserMessage {
	testingContext.Helper()
	messageValue, err := llm.NewUserMessage(llm.UserMessageInput{
		Content: []llm.ContentBlock{
			llm.NewTextBlock(textValue),
		},
		Source: llm.UserMessageSource{},
	})
	if err != nil {
		testingContext.Fatal(err)
	}
	return messageValue
}
