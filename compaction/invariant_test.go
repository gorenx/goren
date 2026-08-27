package compaction

import (
	"context"
	"strings"
	"testing"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
)

const (
	testCompactionID = ID("test-compaction")
	nextCompactionID = ID("next-compaction")
)

func TestInspectLogAcceptsSuccessfulFailedAndOpenAttempts(t *testing.T) {
	t.Parallel()

	numbered := newCompactionSession(t, "numbered-success")
	appendTurnStart(t, numbered, 1)
	appendCompactionStart(t, numbered, testCompactionID, nil, pointerToTurn(1))
	appendCompactionSummary(t, numbered, testCompactionID, nil, []int64{9, 2})
	appendCompactionEnd(t, numbered, testCompactionID, nil, pointerToTurn(1), nil)
	appendTurnEnd(t, numbered, 1)
	inspection, err := InspectLog(numbered.Events())
	if err != nil {
		t.Fatal(err)
	}
	if inspection.OpenTurn != nil || inspection.Attempt != nil {
		t.Fatalf("closed inspection = %#v", inspection)
	}

	standalone := newCompactionSession(t, "standalone-failed")
	appendCompactionStart(t, standalone, testCompactionID, nil, nil)
	failureText := "provider failed"
	appendCompactionEnd(t, standalone, testCompactionID, nil, nil, &failureText)
	if _, err := InspectLog(standalone.Events()); err != nil {
		t.Fatal(err)
	}

	openTail := newCompactionSession(t, "open-attempt")
	commandID := "command-1"
	appendTurnStart(t, openTail, 7)
	startSeq := appendCompactionStart(
		t,
		openTail,
		testCompactionID,
		&commandID,
		pointerToTurn(7),
	)
	inspection, err = InspectLog(openTail.Events())
	if err != nil {
		t.Fatal(err)
	}
	if inspection.OpenTurn == nil || *inspection.OpenTurn != 7 ||
		inspection.Attempt == nil || inspection.Attempt.StartSeq != startSeq ||
		inspection.Attempt.CompactionID != testCompactionID ||
		inspection.Attempt.SourceCommandID == nil ||
		*inspection.Attempt.SourceCommandID != commandID || inspection.Attempt.Summarized {
		t.Fatalf("open inspection = %#v", inspection)
	}
}

func TestInspectLogValidatesCheckpointAgainstOpenAttempt(t *testing.T) {
	t.Parallel()
	conversation := newCompactionSession(t, "checkpoint-valid")
	originalSeq := appendCompactionUser(t, conversation, "original")
	appendTurnStart(t, conversation, 1)
	appendCompactionStart(t, conversation, testCompactionID, nil, pointerToTurn(1))
	appendCompactionSummary(t, conversation, testCompactionID, nil, []int64{originalSeq})
	appendCheckpointReplacement(t, conversation, originalSeq, testCompactionID, nil)
	appendCompactionEnd(t, conversation, testCompactionID, nil, pointerToTurn(1), nil)
	if _, err := InspectLog(conversation.Events()); err != nil {
		t.Fatal(err)
	}

	withoutStart := newCompactionSession(t, "checkpoint-without-start")
	originalSeq = appendCompactionUser(t, withoutStart, "original")
	appendCheckpointReplacement(t, withoutStart, originalSeq, testCompactionID, nil)
	assertInspectionError(t, withoutStart, "checkpoint has no matching compaction/start")

	wrongID := newCompactionSession(t, "checkpoint-wrong-id")
	originalSeq = appendCompactionUser(t, wrongID, "original")
	appendTurnStart(t, wrongID, 1)
	appendCompactionStart(t, wrongID, testCompactionID, nil, pointerToTurn(1))
	appendCheckpointReplacement(t, wrongID, originalSeq, nextCompactionID, nil)
	assertInspectionError(t, wrongID, "does not match compaction/start id")
}

func TestInspectLogRejectsInvalidLifecycleRelationships(t *testing.T) {
	t.Parallel()
	commandID := "command-1"
	nextCommandID := "command-2"
	failureText := "failed"
	testCases := []struct {
		name      string
		populate  func(*testing.T, session.Context)
		wantError string
	}{
		{
			name: "numbered start outside turn",
			populate: func(testingContext *testing.T, conversation session.Context) {
				appendCompactionStart(
					testingContext,
					conversation,
					testCompactionID,
					nil,
					pointerToTurn(1),
				)
			},
			wantError: "outside any open turn",
		},
		{
			name: "standalone start inside turn",
			populate: func(testingContext *testing.T, conversation session.Context) {
				appendTurnStart(testingContext, conversation, 1)
				appendCompactionStart(testingContext, conversation, testCompactionID, nil, nil)
			},
			wantError: "standalone but turn 1 is open",
		},
		{
			name: "nested start",
			populate: func(testingContext *testing.T, conversation session.Context) {
				appendTurnStart(testingContext, conversation, 1)
				appendCompactionStart(
					testingContext,
					conversation,
					testCompactionID,
					nil,
					pointerToTurn(1),
				)
				appendCompactionStart(
					testingContext,
					conversation,
					nextCompactionID,
					nil,
					pointerToTurn(1),
				)
			},
			wantError: "still compacting",
		},
		{
			name: "summary without start",
			populate: func(testingContext *testing.T, conversation session.Context) {
				appendCompactionSummary(
					testingContext,
					conversation,
					testCompactionID,
					nil,
					[]int64{1},
				)
			},
			wantError: "summary has no matching compaction/start",
		},
		{
			name: "summary id mismatch",
			populate: func(testingContext *testing.T, conversation session.Context) {
				appendTurnStart(testingContext, conversation, 1)
				appendCompactionStart(
					testingContext,
					conversation,
					testCompactionID,
					nil,
					pointerToTurn(1),
				)
				appendCompactionSummary(
					testingContext,
					conversation,
					nextCompactionID,
					nil,
					[]int64{1},
				)
			},
			wantError: "summary id next-compaction does not match",
		},
		{
			name: "summary command mismatch",
			populate: func(testingContext *testing.T, conversation session.Context) {
				appendTurnStart(testingContext, conversation, 1)
				appendCompactionStart(
					testingContext,
					conversation,
					testCompactionID,
					&commandID,
					pointerToTurn(1),
				)
				appendCompactionSummary(
					testingContext,
					conversation,
					testCompactionID,
					&nextCommandID,
					[]int64{1},
				)
			},
			wantError: "sourceCommandId command-2 does not match",
		},
		{
			name: "repeated summary",
			populate: func(testingContext *testing.T, conversation session.Context) {
				appendTurnStart(testingContext, conversation, 1)
				appendCompactionStart(
					testingContext,
					conversation,
					testCompactionID,
					nil,
					pointerToTurn(1),
				)
				appendCompactionSummary(
					testingContext,
					conversation,
					testCompactionID,
					nil,
					[]int64{1},
				)
				appendCompactionSummary(
					testingContext,
					conversation,
					testCompactionID,
					nil,
					[]int64{1},
				)
			},
			wantError: "repeated within one compaction",
		},
		{
			name: "empty shadow set",
			populate: func(testingContext *testing.T, conversation session.Context) {
				appendTurnStart(testingContext, conversation, 1)
				appendCompactionStart(
					testingContext,
					conversation,
					testCompactionID,
					nil,
					pointerToTurn(1),
				)
				appendCompactionSummary(
					testingContext,
					conversation,
					testCompactionID,
					nil,
					[]int64{},
				)
			},
			wantError: "shadowedSeqs must be non-empty",
		},
		{
			name: "wrong shadow endpoints",
			populate: func(testingContext *testing.T, conversation session.Context) {
				appendTurnStart(testingContext, conversation, 1)
				appendCompactionStart(
					testingContext,
					conversation,
					testCompactionID,
					nil,
					pointerToTurn(1),
				)
				{
					draft, err := session.NewEventDraft(SummaryEvent,
						validSummaryValue(testCompactionID, nil, []int64{1, 2}, SurfaceRange{
							Start: 1,
							End:   3,
						}))
					if err == nil {
						_, err = conversation.Commit(context.Background(), session.Batch(draft))
					}
					if err != nil {
						testingContext.Fatal(err)
					}
				}
			},
			wantError: "shadowedRange must match",
		},
		{
			name: "end wrong owner",
			populate: func(testingContext *testing.T, conversation session.Context) {
				appendTurnStart(testingContext, conversation, 1)
				appendCompactionStart(
					testingContext,
					conversation,
					testCompactionID,
					nil,
					pointerToTurn(1),
				)
				appendCompactionEnd(
					testingContext,
					conversation,
					testCompactionID,
					nil,
					pointerToTurn(2),
					&failureText,
				)
			},
			wantError: "owner 2 does not match",
		},
		{
			name: "successful end without summary",
			populate: func(testingContext *testing.T, conversation session.Context) {
				appendTurnStart(testingContext, conversation, 1)
				appendCompactionStart(
					testingContext,
					conversation,
					testCompactionID,
					nil,
					pointerToTurn(1),
				)
				appendCompactionEnd(
					testingContext,
					conversation,
					testCompactionID,
					nil,
					pointerToTurn(1),
					nil,
				)
			},
			wantError: "requires one compaction/summary",
		},
		{
			name: "turn boundary crosses numbered attempt",
			populate: func(testingContext *testing.T, conversation session.Context) {
				appendTurnStart(testingContext, conversation, 1)
				appendCompactionStart(
					testingContext,
					conversation,
					testCompactionID,
					nil,
					pointerToTurn(1),
				)
				appendTurnEnd(testingContext, conversation, 1)
			},
			wantError: "turn/end cannot cross an open compaction for turn 1",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			conversation := newCompactionSession(t, session.SessionID(testCase.name))
			testCase.populate(t, conversation)
			assertInspectionError(t, conversation, testCase.wantError)
		})
	}
}

func TestInspectLogEndSeedClearsOnlyInheritedOpenAttempt(t *testing.T) {
	t.Parallel()
	standaloneSource := newCompactionSession(t, "stale-standalone-source")
	appendCompactionStart(t, standaloneSource, testCompactionID, nil, nil)
	replayed, err := session.New("stale-standalone-replay", session.CreateOptions{
		Seed: standaloneSource.Events(),
	})
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := InspectLog(replayed.Events())
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Attempt != nil {
		t.Fatalf("end-seed retained attempt = %#v", inspection.Attempt)
	}

	repairSource := newCompactionSession(t, "repair-source")
	appendCompactionStart(t, repairSource, testCompactionID, nil, nil)
	appendTurnStart(t, repairSource, 1)
	appendTurnEnd(t, repairSource, 1)
	repaired, err := session.New("repair-replay", session.CreateOptions{
		Seed: repairSource.Events(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := InspectLog(repaired.Events()); err != nil {
		t.Fatal(err)
	}

	closedSource := newCompactionSession(t, "closed-source")
	appendCompactionStart(t, closedSource, testCompactionID, nil, nil)
	appendTurnStart(t, closedSource, 1)
	appendTurnEnd(t, closedSource, 1)
	failureText := "failed after crossing turn"
	appendCompactionEnd(t, closedSource, testCompactionID, nil, nil, &failureText)
	closedReplay, err := session.New("closed-replay", session.CreateOptions{
		Seed: closedSource.Events(),
	})
	if err != nil {
		t.Fatal(err)
	}
	assertInspectionError(t, closedReplay, "turn/start cannot cross an open standalone compaction")
}

func appendTurnStart(
	testingContext *testing.T,
	conversation session.Context,
	turnValue int64,
) {
	testingContext.Helper()
	{
		draft, err := session.NewEventDraft(session.TurnStarted,
			session.TurnStart{
				Turn: turnValue,
			})
		if err == nil {
			_, err = conversation.Commit(context.Background(), session.Batch(draft))
		}
		if err != nil {
			testingContext.Fatal(err)
		}
	}
}

func appendTurnEnd(
	testingContext *testing.T,
	conversation session.Context,
	turnValue int64,
) {
	testingContext.Helper()
	{
		draft, err := session.NewEventDraft(session.TurnEnded,
			session.TurnEnd{
				Turn:   turnValue,
				Reason: session.TurnCompleted{},
			})
		if err == nil {
			_, err = conversation.Commit(context.Background(), session.Batch(draft))
		}
		if err != nil {
			testingContext.Fatal(err)
		}
	}
}

func appendCompactionStart(
	testingContext *testing.T,
	conversation session.Context,
	compactionID ID,
	sourceCommandID *string,
	turnValue *int64,
) int64 {
	testingContext.Helper()
	draft, err := session.NewEventDraft(StartEvent,
		Start{
			CompactionID:    compactionID,
			SourceCommandID: sourceCommandID,
			Turn:            turnValue,
		})
	if err != nil {
		testingContext.Fatal(err)
	}
	receipt, err := conversation.Commit(context.Background(), session.Batch(draft))
	if err != nil {
		testingContext.Fatal(err)
	}
	return receipt.Events[0].Seq
}

func appendCompactionSummary(
	testingContext *testing.T,
	conversation session.Context,
	compactionID ID,
	sourceCommandID *string,
	shadowedSeqs []int64,
) {
	testingContext.Helper()
	rangeValue := SurfaceRange{}
	if len(shadowedSeqs) != 0 {
		rangeValue = SurfaceRange{
			Start: shadowedSeqs[0],
			End:   shadowedSeqs[len(shadowedSeqs)-1],
		}
	}
	{
		draft, err := session.NewEventDraft(SummaryEvent,
			validSummaryValue(compactionID, sourceCommandID, shadowedSeqs, rangeValue))
		if err == nil {
			_, err = conversation.Commit(context.Background(), session.Batch(draft))
		}
		if err != nil {
			testingContext.Fatal(err)
		}
	}
}

func validSummaryValue(
	compactionID ID,
	sourceCommandID *string,
	shadowedSeqs []int64,
	rangeValue SurfaceRange,
) Summary {
	return Summary{
		CompactionID:    compactionID,
		SourceCommandID: sourceCommandID,
		Summary: []agentmessage.ContentBlock{
			agentmessage.NewTextBlock("short"),
		},
		ShadowedRange:      rangeValue,
		ShadowedSeqs:       shadowedSeqs,
		ShadowedTokenCount: 12,
		Provider:           "mock",
		Model:              "model-a",
	}
}

func appendCompactionEnd(
	testingContext *testing.T,
	conversation session.Context,
	compactionID ID,
	sourceCommandID *string,
	turnValue *int64,
	failureText *string,
) {
	testingContext.Helper()
	{
		draft, err := session.NewEventDraft(EndEvent,
			End{
				CompactionID:    compactionID,
				SourceCommandID: sourceCommandID,
				Turn:            turnValue,
				Error:           failureText,
			})
		if err == nil {
			_, err = conversation.Commit(context.Background(), session.Batch(draft))
		}
		if err != nil {
			testingContext.Fatal(err)
		}
	}
}

func appendCheckpointReplacement(
	testingContext *testing.T,
	conversation session.Context,
	originalSeq int64,
	compactionID ID,
	sourceCommandID *string,
) {
	testingContext.Helper()
	origin, err := NewCheckpointSource(compactionID, sourceCommandID)
	if err != nil {
		testingContext.Fatal(err)
	}
	messageValue, err := agentmessage.NewUserMessage(agentmessage.UserMessageInput{
		Content: []agentmessage.ContentBlock{
			agentmessage.NewTextBlock("checkpoint"),
		},
		Source: origin,
	})
	if err != nil {
		testingContext.Fatal(err)
	}
	sources := []int64{originalSeq}
	{
		draft, err := session.NewSurfaceEventDraft(session.UserMessageAdded,
			messageValue,
			session.SurfaceIntent{
				Operation:       session.SurfaceReplace(originalSeq, originalSeq),
				SourceEventSeqs: &sources,
			})
		if err == nil {
			_, err = conversation.Commit(context.Background(), session.Batch(draft))
		}
		if err != nil {
			testingContext.Fatal(err)
		}
	}
}

func assertInspectionError(
	testingContext *testing.T,
	conversation session.Context,
	wantError string,
) {
	testingContext.Helper()
	_, err := InspectLog(conversation.Events())
	if err == nil || !strings.Contains(err.Error(), wantError) {
		testingContext.Fatalf("inspection error = %v, want %q", err, wantError)
	}
}

func pointerToTurn(value int64) *int64 {
	return &value
}
