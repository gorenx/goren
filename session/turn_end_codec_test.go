package session

import (
	"encoding/json"
	"testing"

	"github.com/gorenx/goren/llm"
)

func TestTurnEndCodecRestoresClosedReasonUnion(t *testing.T) {
	testCases := []struct {
		name       string
		turnEnding TurnEndReason
	}{
		{
			name:       "completed",
			turnEnding: TurnCompleted{},
		},
		{
			name:       "blocked",
			turnEnding: TurnBlocked{},
		},
		{
			name:       "max tokens",
			turnEnding: TurnMaxTokens{},
		},
		{
			name:       "interrupted",
			turnEnding: TurnInterrupted{},
		},
		{
			name: "user aborted",
			turnEnding: TurnAborted{
				Reason: UserCancelCause{},
			},
		},
		{
			name: "parent aborted",
			turnEnding: TurnAborted{
				Reason: ParentCancelCause{},
			},
		},
		{
			name: "disposed",
			turnEnding: TurnAborted{
				Reason: DisposedCancelCause{},
			},
		},
		{
			name: "hook",
			turnEnding: TurnAborted{
				Reason: HookCancelCause{
					Reason: "policy",
				},
			},
		},
		{
			name: "error",
			turnEnding: TurnError{
				Error: llm.LlmFailure{
					Code:    "MODEL",
					Message: "failed",
				},
			},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			encoded, err := json.Marshal(TurnEnd{
				Turn:   4,
				Reason: testCase.turnEnding,
			})
			if err != nil {
				t.Fatal(err)
			}
			var restored TurnEnd
			if err := json.Unmarshal(encoded, &restored); err != nil {
				t.Fatal(err)
			}
			if restored.Turn != 4 ||
				restored.Reason.TurnEndKind() != testCase.turnEnding.TurnEndKind() {
				t.Fatalf("restored turn/end = %#v", restored)
			}
			originalAbort, originalAborted := testCase.turnEnding.(TurnAborted)
			restoredAbort, restoredAborted := restored.Reason.(TurnAborted)
			if originalAborted != restoredAborted ||
				originalAborted && originalAbort.Reason.CancelKind() != restoredAbort.Reason.CancelKind() {
				t.Fatalf("restored cancellation = %#v", restored.Reason)
			}
		})
	}
}

func TestTurnEndCodecRejectsInvalidUnionValues(t *testing.T) {
	invalidValues := []string{
		`{"turn":0,"reason":{"kind":"completed"}}`,
		`{"turn":1,"reason":{"kind":"unknown"}}`,
		`{"turn":1,"reason":{"kind":"completed","extra":true}}`,
		`{"turn":1,"reason":{"kind":"aborted","reason":{"kind":"hook","reason":""}}}`,
	}
	for _, rawValue := range invalidValues {
		var restored TurnEnd
		if err := json.Unmarshal([]byte(rawValue), &restored); err == nil {
			t.Fatalf("invalid turn/end was accepted: %s", rawValue)
		}
	}
}
