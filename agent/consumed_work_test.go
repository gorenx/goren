package agent

import (
	"testing"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
)

func TestFoldConsumedWorkReportsLatestSteppedTurn(t *testing.T) {
	conversation := consumedWorkSession(t, "stepped")
	appendSteppedTurn(t, conversation, 1, session.TurnCompleted{})
	appendSteppedTurn(t, conversation, 2, session.TurnMaxTokens{})

	work, err := FoldConsumedWork(conversation.Events())
	if err != nil {
		t.Fatal(err)
	}
	if work.End == nil || work.End.Turn != 2 || work.End.Reason.TurnEndKind() != "max-tokens" {
		t.Fatalf("consumed work = %#v", work)
	}
}

func TestFoldConsumedWorkAccountsForClaimedPreStepFailure(t *testing.T) {
	testCases := []struct {
		name       string
		turnEnding session.TurnEndReason
	}{
		{
			name: "error",
			turnEnding: session.TurnError{
				Error: llm.LlmFailure{
					Code:    "MODEL",
					Message: "failed",
				},
			},
		},
		{
			name: "aborted",
			turnEnding: session.TurnAborted{
				Reason: session.UserCancelCause{},
			},
		},
		{
			name:       "blocked",
			turnEnding: session.TurnBlocked{},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			conversation := consumedWorkSession(
				t,
				session.SessionID(testCase.name),
			)
			appendSteppedTurn(t, conversation, 1, session.TurnCompleted{})
			appendTurnStart(t, conversation, 2)
			appendClaim(t, conversation)
			appendTurnEnd(t, conversation, 2, testCase.turnEnding)

			work, err := FoldConsumedWork(conversation.Events())
			if err != nil {
				t.Fatal(err)
			}
			if work.End == nil || work.End.Turn != 2 {
				t.Fatalf("consumed work = %#v", work)
			}
		})
	}
}

func TestFoldConsumedWorkIgnoresTurnWithoutClaimOrStep(t *testing.T) {
	conversation := consumedWorkSession(t, "no-claim")
	appendSteppedTurn(t, conversation, 1, session.TurnCompleted{})
	appendTurnStart(t, conversation, 2)
	appendTurnEnd(t, conversation, 2, session.TurnAborted{
		Reason: session.ParentCancelCause{},
	})
	appendTurnStart(t, conversation, 3)
	appendTurnEnd(t, conversation, 3, session.TurnError{
		Error: llm.LlmFailure{
			Code:    "MODEL",
			Message: "failed",
		},
	})
	appendTurnStart(t, conversation, 4)
	appendTurnEnd(t, conversation, 4, session.TurnBlocked{})

	work, err := FoldConsumedWork(conversation.Events())
	if err != nil {
		t.Fatal(err)
	}
	if work.End == nil || work.End.Turn != 1 {
		t.Fatalf("consumed work = %#v", work)
	}
}

func TestFoldConsumedWorkIgnoresCompletedEmptyClaim(t *testing.T) {
	conversation := consumedWorkSession(t, "empty-claim")
	appendSteppedTurn(t, conversation, 1, session.TurnCompleted{})
	appendTurnStart(t, conversation, 2)
	appendClaim(t, conversation)
	appendTurnEnd(t, conversation, 2, session.TurnCompleted{})

	work, err := FoldConsumedWork(conversation.Events())
	if err != nil {
		t.Fatal(err)
	}
	if work.End == nil || work.End.Turn != 1 {
		t.Fatalf("consumed work = %#v", work)
	}
}

func TestFoldConsumedWorkDoesNotAttributeMidTurnSuffixClaim(t *testing.T) {
	conversation := consumedWorkSession(t, "suffix")
	appendClaim(t, conversation)
	appendTurnEnd(t, conversation, 2, session.TurnAborted{
		Reason: session.UserCancelCause{},
	})

	work, err := FoldConsumedWork(conversation.Events())
	if err != nil {
		t.Fatal(err)
	}
	if work.End != nil {
		t.Fatalf("consumed work = %#v", work)
	}
}

func TestFoldConsumedWorkReportsOnlyUnrunCancellation(t *testing.T) {
	conversation := consumedWorkSession(t, "cancel")
	appendAccepted(t, conversation, "never runs")
	appendCancellation(t, conversation, nil)

	work, err := FoldConsumedWork(conversation.Events())
	if err != nil {
		t.Fatal(err)
	}
	if work.End != nil || !work.DroppedUnrun {
		t.Fatalf("consumed work = %#v", work)
	}

	replaced := consumedWorkSession(t, "replacement")
	replacement := consumedMessage(t, "rewritten")
	appendCancellation(t, replaced, []llm.UserMessage{replacement})
	work, err = FoldConsumedWork(replaced.Events())
	if err != nil {
		t.Fatal(err)
	}
	if work.DroppedUnrun {
		t.Fatal("replacement was counted as unrun cancellation")
	}
}

func TestFoldConsumedWorkLetsLaterTurnAbsorbEarlierDrop(t *testing.T) {
	conversation := consumedWorkSession(t, "absorbed")
	appendAccepted(t, conversation, "canceled")
	appendCancellation(t, conversation, nil)
	appendSteppedTurn(t, conversation, 1, session.TurnCompleted{})

	work, err := FoldConsumedWork(conversation.Events())
	if err != nil {
		t.Fatal(err)
	}
	if work.End == nil || work.End.Turn != 1 || work.DroppedUnrun {
		t.Fatalf("consumed work = %#v", work)
	}
}

func TestFoldConsumedWorkRejectsDamagedTurnEnd(t *testing.T) {
	_, err := FoldConsumedWork([]session.Event{
		{
			Type: session.TurnEndEventName,
			Seq:  7,
			Data: []byte(`{"turn":1,"reason":{"kind":"unknown"}}`),
		},
	})
	if err == nil {
		t.Fatal("damaged turn/end was accepted")
	}
}

func consumedWorkSession(t *testing.T, identifier session.SessionID) *session.Session {
	t.Helper()
	conversation, err := session.New(
		identifier,
		session.CreateOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	return conversation
}

func consumedMessage(t *testing.T, text string) llm.UserMessage {
	t.Helper()
	messageValue, err := llm.NewUserMessage(llm.UserMessageInput{
		Content: []llm.ContentBlock{
			llm.NewTextBlock(text),
		},
		Source: llm.UserMessageSource{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return messageValue
}

func appendAccepted(t *testing.T, conversation *session.Session, text string) {
	t.Helper()
	if _, err := session.Append(
		conversation,
		InboxSpliced,
		InboxSplice{
			Target:   NextTurn,
			Start:    0,
			Inserted: []llm.UserMessage{consumedMessage(t, text)},
		},
	); err != nil {
		t.Fatal(err)
	}
}

func appendClaim(t *testing.T, conversation *session.Session) {
	t.Helper()
	removedCount := 1
	if _, err := session.Append(
		conversation,
		InboxSpliced,
		InboxSplice{
			Target:       NextTurn,
			Start:        0,
			RemovedCount: &removedCount,
			Inserted:     []llm.UserMessage{},
		},
	); err != nil {
		t.Fatal(err)
	}
}

func appendCancellation(
	t *testing.T,
	conversation *session.Session,
	inserted []llm.UserMessage,
) {
	t.Helper()
	if inserted == nil {
		inserted = []llm.UserMessage{}
	}
	removedCount := 1
	if _, err := session.Append(
		conversation,
		InboxSpliced,
		InboxSplice{
			Target:       NextTurn,
			Start:        0,
			RemovedCount: &removedCount,
			Inserted:     inserted,
			Outcome:      InboxCanceled,
		},
	); err != nil {
		t.Fatal(err)
	}
}

func appendSteppedTurn(
	t *testing.T,
	conversation *session.Session,
	turnNumber int64,
	turnEnding session.TurnEndReason,
) {
	t.Helper()
	appendTurnStart(t, conversation, turnNumber)
	appendClaim(t, conversation)
	if _, err := session.Append(
		conversation,
		session.StepStarted,
		session.StepPosition{
			Turn: turnNumber,
			Step: 1,
		},
	); err != nil {
		t.Fatal(err)
	}
	appendTurnEnd(t, conversation, turnNumber, turnEnding)
}

func appendTurnStart(
	t *testing.T,
	conversation *session.Session,
	turnNumber int64,
) {
	t.Helper()
	if _, err := session.Append(
		conversation,
		session.TurnStarted,
		session.TurnStart{
			Turn: turnNumber,
		},
	); err != nil {
		t.Fatal(err)
	}
}

func appendTurnEnd(
	t *testing.T,
	conversation *session.Session,
	turnNumber int64,
	turnEnding session.TurnEndReason,
) {
	t.Helper()
	if _, err := session.Append(
		conversation,
		session.TurnEnded,
		session.TurnEnd{
			Turn:   turnNumber,
			Reason: turnEnding,
		},
	); err != nil {
		t.Fatal(err)
	}
}
