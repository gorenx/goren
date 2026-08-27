package compaction

import (
	"context"
	"testing"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
)

func TestToolPairingBalanceUsesCurrentSurfacePositions(t *testing.T) {
	t.Parallel()
	conversation := newCompactionSession(t, "tool-pairing")
	beforeSeq := appendCompactionUser(t, conversation, "before")
	assistantValue, err := agentmessage.NewAssistantMessage(agentmessage.AssistantMessageInput{
		Content: []agentmessage.ContentBlock{
			agentmessage.ToolCallBlock{
				ID:        "call-1",
				Name:      "read",
				Arguments: `{}`,
			},
		},
		Source: agentmessage.ModelMessageSource{
			Provider: "mock",
			Model:    "model-a",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	emptySources := []int64{}
	assistantEntry, err := session.Event{}, error(nil)
	{
		var committedEvent session.Event
		var writeErr error
		draft, draftErr := session.NewSurfaceEventDraft(session.AssistantMessaged,
			session.AssistantMessage{
				Turn:    1,
				Step:    1,
				Message: assistantValue,
			},
			session.SurfaceIntent{
				Operation:       session.SurfaceAppend(),
				SourceEventSeqs: &emptySources,
			})
		writeErr = draftErr
		if draftErr == nil {
			receipt, commitErr := conversation.Commit(context.Background(), session.Batch(draft))
			writeErr = commitErr
			if commitErr == nil {
				committedEvent = receipt.Events[0]
			}
		}
		assistantEntry = committedEvent
		err = writeErr
	}

	if err != nil {
		t.Fatal(err)
	}
	resultValue, err := agentmessage.NewToolResultMessage(agentmessage.ToolResultMessageInput{
		CallID: "call-1",
		Content: []agentmessage.ContentBlock{
			agentmessage.NewTextBlock("result"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	resultEntry, err := session.Event{}, error(nil)
	{
		var committedEvent session.Event
		var writeErr error
		draft, draftErr := session.NewSurfaceEventDraft(session.ToolResultAdded,
			session.ToolResult{
				Turn:    1,
				Step:    1,
				Message: resultValue,
			},
			session.SurfaceIntent{
				Operation: session.SurfaceAppend(),
			})
		writeErr = draftErr
		if draftErr == nil {
			receipt, commitErr := conversation.Commit(context.Background(), session.Batch(draft))
			writeErr = commitErr
			if commitErr == nil {
				committedEvent = receipt.Events[0]
			}
		}
		resultEntry = committedEvent
		err = writeErr
	}

	if err != nil {
		t.Fatal(err)
	}
	afterSeq := appendCompactionUser(t, conversation, "after")
	boundaries, err := BuildToolPairingBoundaries(conversation.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	beforeAssistant, err := boundaries.CutBefore(assistantEntry.Seq)
	if err != nil {
		t.Fatal(err)
	}
	afterAssistant, err := boundaries.CutAfter(assistantEntry.Seq)
	if err != nil {
		t.Fatal(err)
	}
	if !beforeAssistant || afterAssistant {
		t.Fatalf(
			"indexed assistant cuts = (%t, %t), want (true, false)",
			beforeAssistant,
			afterAssistant,
		)
	}

	assertToolPairingCut(t, conversation, beforeSeq, true, true)
	assertToolPairingCut(t, conversation, assistantEntry.Seq, true, false)
	assertToolPairingCut(t, conversation, resultEntry.Seq, false, true)
	assertToolPairingCut(t, conversation, afterSeq, true, true)

	replacement := mustCompactionUserMessage(t, "summary")
	replacedSeqs := []int64{beforeSeq}
	replacementEntry, err := session.Event{}, error(nil)
	{
		var committedEvent session.Event
		var writeErr error
		draft, draftErr := session.NewSurfaceEventDraft(session.UserMessageAdded,
			replacement,
			session.SurfaceIntent{
				Operation:       session.SurfaceReplace(beforeSeq, beforeSeq),
				SourceEventSeqs: &replacedSeqs,
			})
		writeErr = draftErr
		if draftErr == nil {
			receipt, commitErr := conversation.Commit(context.Background(), session.Batch(draft))
			writeErr = commitErr
			if commitErr == nil {
				committedEvent = receipt.Events[0]
			}
		}
		replacementEntry = committedEvent
		err = writeErr
	}

	if err != nil {
		t.Fatal(err)
	}
	assertToolPairingCut(t, conversation, replacementEntry.Seq, true, true)
	assertToolPairingCut(t, conversation, assistantEntry.Seq, true, false)
}

func TestToolPairingBalanceRejectsOrphanResultAndAbsentSequence(t *testing.T) {
	t.Parallel()
	conversation := newCompactionSession(t, "tool-orphan")
	resultValue, err := agentmessage.NewToolResultMessage(agentmessage.ToolResultMessageInput{
		CallID:  "orphan",
		Content: []agentmessage.ContentBlock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	resultEntry, err := session.Event{}, error(nil)
	{
		var committedEvent session.Event
		var writeErr error
		draft, draftErr := session.NewSurfaceEventDraft(session.ToolResultAdded,
			session.ToolResult{
				Turn:    1,
				Step:    1,
				Message: resultValue,
			},
			session.SurfaceIntent{
				Operation: session.SurfaceAppend(),
			})
		writeErr = draftErr
		if draftErr == nil {
			receipt, commitErr := conversation.Commit(context.Background(), session.Batch(draft))
			writeErr = commitErr
			if commitErr == nil {
				committedEvent = receipt.Events[0]
			}
		}
		resultEntry = committedEvent
		err = writeErr
	}

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
	conversation session.Context,
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
) session.Context {
	testingContext.Helper()
	conversation, err := session.New(identifier, session.CreateOptions{})
	if err != nil {
		testingContext.Fatal(err)
	}
	return conversation
}

func appendCompactionUser(
	testingContext *testing.T,
	conversation session.Context,
	textValue string,
) int64 {
	testingContext.Helper()
	messageValue := mustCompactionUserMessage(testingContext, textValue)
	committed, err := session.Event{}, error(nil)
	{
		var committedEvent session.Event
		var writeErr error
		draft, draftErr := session.NewSurfaceEventDraft(session.UserMessageAdded,
			messageValue,
			session.SurfaceIntent{
				Operation: session.SurfaceAppend(),
			})
		writeErr = draftErr
		if draftErr == nil {
			receipt, commitErr := conversation.Commit(context.Background(), session.Batch(draft))
			writeErr = commitErr
			if commitErr == nil {
				committedEvent = receipt.Events[0]
			}
		}
		committed = committedEvent
		err = writeErr
	}

	if err != nil {
		testingContext.Fatal(err)
	}
	return committed.Seq
}

func mustCompactionUserMessage(
	testingContext *testing.T,
	textValue string,
) agentmessage.UserMessage {
	testingContext.Helper()
	messageValue, err := agentmessage.NewUserMessage(agentmessage.UserMessageInput{
		Content: []agentmessage.ContentBlock{
			agentmessage.NewTextBlock(textValue),
		},
		Source: agentmessage.UserMessageSource{},
	})
	if err != nil {
		testingContext.Fatal(err)
	}
	return messageValue
}
