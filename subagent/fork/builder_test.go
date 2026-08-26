package fork

import (
	"context"
	"testing"

	"github.com/gorenx/goren/session"
)

func TestCompletedTurnPrefixExcludesInflightTurn(t *testing.T) {
	t.Parallel()
	conversation, err := session.New("parent", session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	appendTurnBoundary(t, conversation, 1, true)
	completedLength := len(conversation.Events())
	appendTurnBoundary(t, conversation, 2, false)
	prefix := completedTurnPrefix(conversation.Events())
	if len(prefix) != completedLength ||
		prefix[len(prefix)-1].Type != session.TurnEndEventName {
		t.Fatalf("prefix = %#v", prefix)
	}
}

func appendTurnBoundary(
	t *testing.T,
	conversation session.Context,
	turn int64,
	completed bool,
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
	if !completed {
		return
	}
	{
		draft, err := session.NewEventDraft(session.TurnEnded,
			session.TurnEnd{
				Turn:   turn,
				Reason: session.TurnCompleted{},
			})
		if err == nil {
			_, err = conversation.Commit(context.Background(), session.Batch(draft))
		}
		if err != nil {
			t.Fatal(err)
		}
	}
}
