package continuation

import (
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
)

func TestEpochStopReasonUsesOnlyCurrentActivationSuffix(t *testing.T) {
	conversation, createErr := session.New(
		"outcome-suffix",
		session.CreateOptions{},
	)
	if createErr != nil {
		t.Fatal(createErr)
	}
	appendConsumedTurn(
		t,
		conversation,
		1,
		session.TurnError{
			Error: llm.LlmFailure{
				Code:    "OLD",
				Message: "old failure",
			},
		},
	)
	boundary := conversation.Seq()
	appendConsumedTurn(t, conversation, 2, session.TurnCompleted{})

	actual := epochStopReason(
		conversation,
		boundary,
		subagent.StopCompleted,
	)
	if actual != subagent.StopCompleted {
		t.Fatalf("epoch stop reason = %q", actual)
	}
}

func TestEpochStopReasonMapsConsumedWorkOutcomes(t *testing.T) {
	testCases := []struct {
		name       string
		turnEnding session.TurnEndReason
		expected   subagent.StopReason
	}{
		{
			name:       "completed",
			turnEnding: session.TurnCompleted{},
			expected:   subagent.StopCompleted,
		},
		{
			name:       "refusal",
			turnEnding: session.TurnBlocked{},
			expected:   subagent.StopRefusal,
		},
		{
			name:       "max tokens",
			turnEnding: session.TurnMaxTokens{},
			expected:   subagent.StopMaxTokens,
		},
		{
			name:       "interrupted",
			turnEnding: session.TurnInterrupted{},
			expected:   subagent.StopAborted,
		},
		{
			name: "aborted",
			turnEnding: session.TurnAborted{
				Reason: session.ParentCancelCause{},
			},
			expected: subagent.StopAborted,
		},
		{
			name: "error",
			turnEnding: session.TurnError{
				Error: llm.LlmFailure{
					Code:    "MODEL",
					Message: "failed",
				},
			},
			expected: subagent.StopError,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			conversation, createErr := session.New(
				session.SessionID(testCase.name),
				session.CreateOptions{},
			)
			if createErr != nil {
				t.Fatal(createErr)
			}
			appendConsumedTurn(t, conversation, 1, testCase.turnEnding)
			actual := epochStopReason(
				conversation,
				0,
				subagent.StopCompleted,
			)
			if actual != testCase.expected {
				t.Fatalf("epoch stop reason = %q, want %q", actual, testCase.expected)
			}
		})
	}
}

func TestEpochStopReasonReportsUnrunCancellation(t *testing.T) {
	conversation, createErr := session.New(
		"outcome-canceled",
		session.CreateOptions{},
	)
	if createErr != nil {
		t.Fatal(createErr)
	}
	removedCount := 1
	if _, appendErr := session.Append(
		conversation,
		agent.InboxSpliced,
		agent.InboxSplice{
			Target:       agent.NextTurn,
			Start:        0,
			RemovedCount: &removedCount,
			Inserted:     []llm.UserMessage{},
			Outcome:      agent.InboxCanceled,
		},
	); appendErr != nil {
		t.Fatal(appendErr)
	}

	actual := epochStopReason(
		conversation,
		0,
		subagent.StopCompleted,
	)
	if actual != subagent.StopAborted {
		t.Fatalf("epoch stop reason = %q", actual)
	}
}

func appendConsumedTurn(
	t *testing.T,
	conversation *session.Session,
	turnNumber int64,
	turnEnding session.TurnEndReason,
) {
	t.Helper()
	if _, appendErr := session.Append(
		conversation,
		session.TurnStarted,
		session.TurnStart{
			Turn: turnNumber,
		},
	); appendErr != nil {
		t.Fatal(appendErr)
	}
	removedCount := 1
	if _, appendErr := session.Append(
		conversation,
		agent.InboxSpliced,
		agent.InboxSplice{
			Target:       agent.NextTurn,
			Start:        0,
			RemovedCount: &removedCount,
			Inserted:     []llm.UserMessage{},
		},
	); appendErr != nil {
		t.Fatal(appendErr)
	}
	if _, appendErr := session.Append(
		conversation,
		session.StepStarted,
		session.StepPosition{
			Turn: turnNumber,
			Step: 1,
		},
	); appendErr != nil {
		t.Fatal(appendErr)
	}
	if _, appendErr := session.Append(
		conversation,
		session.TurnEnded,
		session.TurnEnd{
			Turn:   turnNumber,
			Reason: turnEnding,
		},
	); appendErr != nil {
		t.Fatal(appendErr)
	}
}
