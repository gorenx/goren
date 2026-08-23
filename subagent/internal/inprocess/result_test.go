package inprocess

import (
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

func appendCompletedTurn(
	t *testing.T,
	conversation *session.Session,
	turn int64,
	text string,
) {
	t.Helper()
	appendTurn(t, conversation, turn, text, session.TurnCompleted{})
}

func appendTurn(
	t *testing.T,
	conversation *session.Session,
	turn int64,
	text string,
	reason session.TurnEndReason,
) {
	t.Helper()
	if _, err := session.AppendSerialized(
		conversation,
		session.TurnStarted,
		session.TurnStart{
			Turn: turn,
		},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := session.AppendSerialized(
		conversation,
		session.StepStarted,
		session.StepPosition{
			Turn: turn,
			Step: 1,
		},
	); err != nil {
		t.Fatal(err)
	}
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
	if _, err = session.AppendSurfaceSerialized(
		conversation,
		session.AssistantMessaged,
		session.AssistantMessage{
			Turn:    turn,
			Step:    1,
			Message: messageValue,
		},
		session.SurfaceIntent{
			Operation: session.SurfaceAppend(),
		},
	); err != nil {
		t.Fatal(err)
	}
	if _, err = session.AppendSerialized(
		conversation,
		session.StepEnded,
		session.StepPosition{
			Turn: turn,
			Step: 1,
		},
	); err != nil {
		t.Fatal(err)
	}
	if _, err = session.AppendSerialized(
		conversation,
		session.TurnEnded,
		session.TurnEnd{
			Turn:   turn,
			Reason: reason,
		},
	); err != nil {
		t.Fatal(err)
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
