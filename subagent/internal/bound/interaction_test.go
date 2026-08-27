package bound

import (
	"context"
	"testing"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
)

func TestNextParentInteractionFoldsOneCompletedDirectUserTurn(t *testing.T) {
	t.Parallel()
	conversation := newInteractionSession(t)
	appendTurnStart(t, conversation, 1)
	appendInteractionUser(
		t,
		conversation,
		agentmessage.UserMessageSource{},
		"research this",
	)
	appendInteractionAssistant(
		t,
		conversation,
		1,
		1,
		[]agentmessage.ContentBlock{
			agentmessage.ReasoningBlock{
				Text: "private reasoning",
			},
			agentmessage.ToolCallBlock{
				ID:        "call-1",
				Name:      "search",
				Arguments: `{}`,
			},
			agentmessage.NewTextBlock("parent answer"),
		},
	)
	appendTurnEnd(t, conversation, 1, session.TurnCompleted{})

	interaction, found, err := nextParentInteraction(
		conversation.Events(),
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !found || !interaction.deliverable || interaction.turn != 1 ||
		interaction.fromSeq != 0 || interaction.nextSeq != 4 ||
		interaction.outcome != "completed" {
		t.Fatalf("interaction = %#v, found = %v", interaction, found)
	}
	if hasInteractionText(interaction.content, "private reasoning") ||
		hasInteractionType(interaction.content, "tool-call") ||
		!hasInteractionText(interaction.content, "research this") ||
		!hasInteractionText(interaction.content, "parent answer") {
		t.Fatalf("interaction content = %#v", interaction.content)
	}
}

func TestNextParentInteractionSkipsTurnsWithoutDirectUserInput(t *testing.T) {
	t.Parallel()
	conversation := newInteractionSession(t)
	appendTurnStart(t, conversation, 1)
	appendInteractionUser(
		t,
		conversation,
		subagent.ReportSource{
			SenderSessionID: "child",
		},
		"child report",
	)
	appendInteractionAssistant(
		t,
		conversation,
		1,
		1,
		[]agentmessage.ContentBlock{
			agentmessage.NewTextBlock("parent handled report"),
		},
	)
	appendTurnEnd(t, conversation, 1, session.TurnCompleted{})

	interaction, found, err := nextParentInteraction(
		conversation.Events(),
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !found || interaction.deliverable || interaction.nextSeq != 4 {
		t.Fatalf("interaction = %#v, found = %v", interaction, found)
	}
}

func TestNextParentInteractionWaitsForTurnEndAndSkipsPartialBaseline(
	t *testing.T,
) {
	t.Parallel()
	conversation := newInteractionSession(t)
	appendTurnStart(t, conversation, 1)
	appendInteractionUser(
		t,
		conversation,
		agentmessage.UserMessageSource{},
		"question",
	)
	if _, found, err := nextParentInteraction(
		conversation.Events(),
		0,
	); err != nil || found {
		t.Fatalf("open interaction = found:%v error:%v", found, err)
	}
	appendTurnEnd(t, conversation, 1, session.TurnBlocked{})
	interaction, found, err := nextParentInteraction(
		conversation.Events(),
		1,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !found || interaction.deliverable || interaction.nextSeq != 3 {
		t.Fatalf("partial interaction = %#v, found = %v", interaction, found)
	}
}

func TestBoundCursorRequiresContiguousPerChildAdvances(t *testing.T) {
	t.Parallel()
	conversation := newInteractionSession(t)
	appendTurnStart(t, conversation, 1)
	appendInteractionUser(
		t,
		conversation,
		agentmessage.UserMessageSource{},
		"question",
	)
	appendTurnEnd(t, conversation, 1, session.TurnBlocked{})
	appendCursor(t, conversation, subagent.BoundCursor{
		Version:         subagent.BoundEventVersion,
		ChildSessionID:  "child",
		PreviousNextSeq: 0,
		NextSeq:         3,
		ThroughTurn:     1,
		Disposition:     subagent.BoundCursorDelivered,
	})
	nextSeq, err := boundCursor(conversation.Events(), "child", 0)
	if err != nil || nextSeq != 3 {
		t.Fatalf("cursor = %d, error = %v", nextSeq, err)
	}
	appendCursor(t, conversation, subagent.BoundCursor{
		Version:         subagent.BoundEventVersion,
		ChildSessionID:  "child",
		PreviousNextSeq: 2,
		NextSeq:         4,
		ThroughTurn:     2,
		Disposition:     subagent.BoundCursorSkipped,
	})
	if _, err = boundCursor(conversation.Events(), "child", 0); err == nil {
		t.Fatal("non-contiguous Bound cursor was accepted")
	}
}

func newInteractionSession(t *testing.T) session.Context {
	t.Helper()
	conversation, err := session.New("parent", session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return conversation
}

func appendTurnStart(
	t *testing.T,
	conversation session.Context,
	turn int64,
) {
	t.Helper()
	draft, err := session.NewEventDraft(
		session.TurnStarted,
		session.TurnStart{
			Turn: turn,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	commitInteractionDraft(t, conversation, draft)
}

func appendInteractionUser(
	t *testing.T,
	conversation session.Context,
	origin agentmessage.MessageSource,
	text string,
) {
	t.Helper()
	messageValue, err := agentmessage.NewUserMessage(
		agentmessage.UserMessageInput{
			Content: []agentmessage.ContentBlock{
				agentmessage.NewTextBlock(text),
			},
			Source: origin,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := session.NewSurfaceEventDraft(
		session.UserMessageAdded,
		messageValue,
		session.SurfaceIntent{
			Operation: session.SurfaceAppend(),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	commitInteractionDraft(t, conversation, draft)
}

func appendInteractionAssistant(
	t *testing.T,
	conversation session.Context,
	turn int64,
	step int64,
	content []agentmessage.ContentBlock,
) {
	t.Helper()
	messageValue, err := agentmessage.NewAssistantMessage(
		agentmessage.AssistantMessageInput{
			Content: content,
			Source: agentmessage.ModelMessageSource{
				Provider: "provider",
				Model:    "model",
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := session.NewSurfaceEventDraft(
		session.AssistantMessaged,
		session.AssistantMessage{
			Turn:    turn,
			Step:    step,
			Message: messageValue,
		},
		session.SurfaceIntent{
			Operation: session.SurfaceAppend(),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	commitInteractionDraft(t, conversation, draft)
}

func appendTurnEnd(
	t *testing.T,
	conversation session.Context,
	turn int64,
	reason session.TurnEndReason,
) {
	t.Helper()
	draft, err := session.NewEventDraft(
		session.TurnEnded,
		session.TurnEnd{
			Turn:   turn,
			Reason: reason,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	commitInteractionDraft(t, conversation, draft)
}

func appendCursor(
	t *testing.T,
	conversation session.Context,
	cursor subagent.BoundCursor,
) {
	t.Helper()
	draft, err := session.NewEventDraft(subagent.BoundCursorEvent, cursor)
	if err != nil {
		t.Fatal(err)
	}
	commitInteractionDraft(t, conversation, draft)
}

func commitInteractionDraft(
	t *testing.T,
	conversation session.Context,
	draft session.EventDraft,
) {
	t.Helper()
	if _, err := conversation.Commit(
		context.Background(),
		session.Batch(draft),
	); err != nil {
		t.Fatal(err)
	}
}

func hasInteractionText(
	content []agentmessage.ContentBlock,
	want string,
) bool {
	for _, block := range content {
		plain, matches := block.(agentmessage.PlainTextContent)
		if !matches {
			continue
		}
		text, present := plain.PlainText()
		if present && text == want {
			return true
		}
	}
	return false
}

func hasInteractionType(
	content []agentmessage.ContentBlock,
	want string,
) bool {
	for _, block := range content {
		if block.ContentType() == want {
			return true
		}
	}
	return false
}
