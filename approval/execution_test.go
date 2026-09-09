package approval

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/gorenx/goren/session"
)

type journal struct {
	history     *session.Log
	failEvent   string
	afterCommit bool
	corrupt     bool
}

func (target *journal) Commit(ctx context.Context, plan session.WritePlan) (session.WriteResult, error) {
	entry := plan.(audit)
	fail := entry.eventType == target.failEvent
	if fail {
		target.failEvent = ""
	}
	if fail && !target.afterCommit {
		return session.WriteResult{}, errors.New("audit submission failed")
	}
	result, err := target.history.Commit(ctx, plan)
	if fail {
		return session.WriteResult{}, errors.New("audit reply lost")
	}
	if target.corrupt {
		target.corrupt = false
		result.Barrier.SessionID = "another-session"
	}
	return result, err
}

type answerer struct {
	history           *session.Log
	calls             int
	answer            Outcome
	cancel            context.CancelFunc
	failure           error
	panics            bool
	askedBeforeAnswer bool
}

func (source *answerer) Answer(_ context.Context, input Question) (Outcome, error) {
	source.calls++
	entries := source.history.Events()
	if len(entries) > 1 && entries[len(entries)-1].Type == AskedEventName {
		var payload Asked
		err := json.Unmarshal(entries[len(entries)-1].Data, &payload)
		source.askedBeforeAnswer = err == nil && payload.ID == input.ID
	}
	if source.cancel != nil {
		source.cancel()
	}
	if source.panics {
		panic("answer provider failed")
	}
	return source.answer, source.failure
}

func beginExecution(t *testing.T, history *session.Log, configuredPolicy Policy) *Execution {
	t.Helper()
	work, err := rulesForTest(t, configuredPolicy).Begin(Request{
		SessionID: history.Header().ID,
		Events:    history.Events(),
		ToolName:  "read",
	})
	if err != nil {
		t.Fatal(err)
	}
	return work
}

func TestExecutionRetriesAuditWithoutRepeatingQuestion(t *testing.T) {
	for _, eventType := range []string{AskedEventName, DecidedEventName} {
		for _, afterCommit := range []bool{false, true} {
			label := eventType + "/submission-failed"
			if afterCommit {
				label = eventType + "/reply-lost"
			}
			t.Run(label, func(t *testing.T) {
				history := openLog(t)
				work := beginExecution(t, history, PolicyAsk)
				target := &journal{
					history:     history,
					failEvent:   eventType,
					afterCommit: afterCommit,
				}
				source := &answerer{
					history: history,
					answer:  OutcomeAllowedOnce,
				}
				decisionValue, err := work.Run(context.Background(), target, source)
				if err == nil || decisionValue != "" {
					t.Fatalf("unconfirmed audit returned an decisionValue: %s %v", decisionValue, err)
				}
				if eventType == AskedEventName && source.calls != 0 {
					t.Fatal("question delivered before asked confirmation")
				}
				// Retry the retained execution. Log idempotency recognizes committed
				// facts even when the first submission's reply was lost.
				for attempt := 0; attempt < 2; attempt++ {
					decisionValue, err = work.Run(context.Background(), target, source)
					if err != nil || decisionValue != OutcomeAllowedOnce {
						t.Fatalf("retry: %s %v", decisionValue, err)
					}
				}
				if source.calls != 1 || !source.askedBeforeAnswer {
					t.Fatalf("question calls=%d askedBeforeAnswer=%v", source.calls, source.askedBeforeAnswer)
				}
				entries := history.Events()
				if len(entries) != 3 || entries[1].Type != AskedEventName || entries[2].Type != DecidedEventName {
					t.Fatalf("audit pair was repeated or incomplete: %+v", entries)
				}
			})
		}
	}
}

func TestExecutionAuditsPolicyAndCancellationOutcomes(t *testing.T) {
	for _, scenario := range []struct {
		name             string
		configuredPolicy Policy
		cancel           bool
		panics           bool
		failed           bool
		answer           Outcome
		want             Outcome
	}{
		{
			name:             "never",
			configuredPolicy: PolicyNever,
			want:             OutcomeRejected,
		},
		{
			name:             "late-allow",
			configuredPolicy: PolicyAsk,
			cancel:           true,
			answer:           OutcomeAllowedOnce,
			want:             OutcomeCancelled,
		},
		{
			name:             "panic",
			configuredPolicy: PolicyAsk,
			panics:           true,
			want:             OutcomeUnavailable,
		},
		{
			name:             "failure",
			configuredPolicy: PolicyAsk,
			failed:           true,
			answer:           OutcomeAllowedOnce,
			want:             OutcomeUnavailable,
		},
		{
			name:             "invalid",
			configuredPolicy: PolicyAsk,
			answer:           "invalid",
			want:             OutcomeUnavailable,
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			history := openLog(t)
			work := beginExecution(t, history, scenario.configuredPolicy)
			target := &journal{
				history: history,
			}
			source := &answerer{
				history: history,
				answer:  scenario.answer,
				panics:  scenario.panics,
			}
			if scenario.cancel {
				source.cancel = cancel
			}
			if scenario.failed {
				source.failure = errors.New("answer unavailable")
			}
			decisionValue, err := work.Run(ctx, target, source)
			if err != nil || decisionValue != scenario.want {
				t.Fatalf("decisionValue %s %v", decisionValue, err)
			}
			entries := history.Events()
			if len(entries) != 3 {
				t.Fatal("audit pair incomplete")
			}
			var recorded Decided
			err = json.Unmarshal(entries[2].Data, &recorded)
			if err != nil || recorded.Outcome != scenario.want {
				t.Fatalf("terminal audit=%+v, %v", recorded, err)
			}
			if scenario.configuredPolicy == PolicyNever && source.calls != 0 {
				t.Fatal("never configuredPolicy invoked the answerer")
			}
		})
	}
}

func TestExecutionRejectsUnrelatedReceiptAndPreservesRetry(t *testing.T) {
	history := openLog(t)
	work := beginExecution(t, history, PolicyAsk)
	target := &journal{
		history: history,
		corrupt: true,
	}
	source := &answerer{
		history: history,
		answer:  OutcomeAllowedOnce,
	}
	decisionValue, err := work.Run(context.Background(), target, source)
	if err == nil || decisionValue != "" || source.calls != 0 {
		t.Fatal("unrelated receipt authorized a question")
	}
	decisionValue, err = work.Run(context.Background(), target, source)
	if err != nil || decisionValue != OutcomeAllowedOnce || history.Seq() != 3 || source.calls != 1 {
		t.Fatalf("retry: %s %v", decisionValue, err)
	}
}

func TestAuditRejectsDifferentSessionAndTurn(t *testing.T) {
	history := openLog(t)
	work := beginExecution(t, history, PolicyNever)
	foreign, err := session.NewLog("foreign", session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = foreign.Commit(context.Background(), work.pending)
	if err == nil {
		t.Fatal("foreign Session accepted audit")
	}
	ending, err := session.NewEventDraft(session.TurnEnded, session.TurnEnd{
		Turn:   1,
		Reason: session.TurnCompleted{},
	})
	if err != nil {
		t.Fatal(err)
	}
	starting, err := session.NewEventDraft(session.TurnStarted, session.TurnStart{
		Turn: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = history.Commit(context.Background(), session.Batch(ending, starting))
	if err != nil {
		t.Fatal(err)
	}
	_, err = history.Commit(context.Background(), work.pending)
	if err == nil {
		t.Fatal("a later Turn adopted the old approval")
	}
}
