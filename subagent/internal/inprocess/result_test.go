package inprocess

import (
	"context"
	"testing"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
)

func TestReadResultUsesOnlyEventsAfterForkBoundary(t *testing.T) {
	t.Parallel()
	parentSession, err := session.New("parent", session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	appendCompletedTurn(t, parentSession, 1, "parent answer")
	seed := parentSession.Events()
	childSession, err := session.New(
		"child",
		session.CreateOptions{
			Seed: seed,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	appendCompletedTurn(t, childSession, 2, "child answer")
	result, err := readResult(
		childSession,
		int64(len(seed)),
		false,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.StopReason != subagent.StopCompleted ||
		visibleContent(result.Output) != "child answer" {
		t.Fatalf("result = %#v", result)
	}
}

func TestReadResultPreservesPartialOutputAndCancellation(t *testing.T) {
	t.Parallel()
	childSession, err := session.New("child", session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	appendTurn(
		t,
		childSession,
		1,
		"partial answer",
		session.TurnMaxTokens{},
	)
	result, err := readResult(childSession, 0, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.StopReason != subagent.StopAborted ||
		visibleContent(result.Output) != "partial answer" {
		t.Fatalf("result = %#v", result)
	}
}

func TestReadResultUsesTextChunksWhenCancellationPreventsMessageCommit(t *testing.T) {
	t.Parallel()
	childSession, err := session.New("partial-child", session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	appendTurnStart(t, childSession, 1)
	appendStepStart(t, childSession, 1)
	{
		draft, err := session.NewEventDraft(session.AssistantChunked,
			session.AssistantChunk{
				Turn: 1,
				Step: 1,
				Chunk: llm.TextDeltaChunk{
					Index: 0,
					Text:  "partial answer",
				},
			})
		if err == nil {
			_, err = childSession.Commit(context.Background(), session.Batch(draft))
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	appendTurnEnd(t, childSession, 1, session.TurnInterrupted{})

	result, err := readResult(childSession, 0, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.StopReason != subagent.StopAborted ||
		visibleContent(result.Output) != "partial answer" {
		t.Fatalf("result = %#v", result)
	}
}

func TestReadResultIgnoresLaterTurnThatConsumedNoWork(t *testing.T) {
	t.Parallel()
	childSession, err := session.New("no-op-child", session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	appendCompletedTurn(t, childSession, 1, "completed answer")
	appendTurnStart(t, childSession, 2)
	appendTurnEnd(t, childSession, 2, session.TurnBlocked{})

	result, err := readResult(childSession, 0, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.StopReason != subagent.StopCompleted ||
		visibleContent(result.Output) != "completed answer" {
		t.Fatalf("result = %#v", result)
	}
}

func appendCompletedTurn(
	t *testing.T,
	conversation session.Context,
	turn int64,
	text string,
) {
	t.Helper()
	appendTurn(t, conversation, turn, text, session.TurnCompleted{})
}

func appendTurn(
	t *testing.T,
	conversation session.Context,
	turn int64,
	text string,
	reason session.TurnEndReason,
) {
	t.Helper()
	appendTurnStart(t, conversation, turn)
	appendStepStart(t, conversation, turn)
	messageValue, err := llm.NewAssistantMessage(llm.AssistantMessageInput{
		Content: []llm.ContentBlock{
			llm.NewTextBlock(text),
		},
		Source: llm.ModelMessageSource{
			Provider: "test",
			Model:    "test",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	{
		var committedEvent session.Event
		var writeErr error
		draft, draftErr := session.NewSurfaceEventDraft(session.AssistantMessaged,
			session.AssistantMessage{
				Turn:    turn,
				Step:    1,
				Message: messageValue,
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
		if _, err = committedEvent, writeErr; err != nil {
			t.Fatal(err)
		}
	}
	{
		var committedEvent session.Event
		var writeErr error
		draft, draftErr := session.NewEventDraft(session.StepEnded,
			session.StepPosition{
				Turn: turn,
				Step: 1,
			})
		writeErr = draftErr
		if draftErr == nil {
			receipt, commitErr := conversation.Commit(context.Background(), session.Batch(draft))
			writeErr = commitErr
			if commitErr == nil {
				committedEvent = receipt.Events[0]
			}
		}
		if _, err = committedEvent, writeErr; err != nil {
			t.Fatal(err)
		}
	}
	appendTurnEnd(t, conversation, turn, reason)
}

func appendTurnStart(
	t *testing.T,
	conversation session.Context,
	turn int64,
) {
	t.Helper()
	{
		draft, err := session.NewEventDraft(session.TurnStarted,
			session.TurnStart{
				Turn: turn,
			})
		if err == nil {
			_, err = conversation.Commit(context.Background(), session.Batch(draft))
		}
		if err != nil {
			t.Fatal(err)
		}
	}
}

func appendStepStart(
	t *testing.T,
	conversation session.Context,
	turn int64,
) {
	t.Helper()
	{
		draft, err := session.NewEventDraft(session.StepStarted,
			session.StepPosition{
				Turn: turn,
				Step: 1,
			})
		if err == nil {
			_, err = conversation.Commit(context.Background(), session.Batch(draft))
		}
		if err != nil {
			t.Fatal(err)
		}
	}
}

func appendTurnEnd(
	t *testing.T,
	conversation session.Context,
	turn int64,
	reason session.TurnEndReason,
) {
	t.Helper()
	{
		draft, err := session.NewEventDraft(session.TurnEnded,
			session.TurnEnd{
				Turn:   turn,
				Reason: reason,
			})
		if err == nil {
			_, err = conversation.Commit(context.Background(), session.Batch(draft))
		}
		if err != nil {
			t.Fatal(err)
		}
	}
}

func visibleContent(content []llm.ContentBlock) string {
	if len(content) != 1 {
		return ""
	}
	plainText, matches := content[0].(llm.PlainTextContent)
	if !matches {
		return ""
	}
	value, _ := plainText.PlainText()
	return value
}
