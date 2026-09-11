package approval

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
)

func rulesForTest(t *testing.T, selected Policy) *Service {
	t.Helper()
	settings, err := ValidateConfig(Config{
		Policy: &selected,
	})
	if err != nil {
		t.Fatal(err)
	}
	rules, err := New(settings)
	if err != nil {
		t.Fatal(err)
	}
	return rules
}

func openLog(t *testing.T) *session.Log {
	t.Helper()
	history, err := session.NewLog("approval", session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	draft, err := session.NewEventDraft(session.TurnStarted, session.TurnStart{
		Turn: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := history.Commit(context.Background(), session.Batch(draft)); err != nil {
		t.Fatal(err)
	}
	return history
}

func TestDecisionProducesCorrelatedAuditWithoutWritingSession(t *testing.T) {
	history := openLog(t)
	rules := rulesForTest(t, PolicyAsk)
	callID := agentmessage.CallID("call")
	reason := "permission"
	prepared, err := rules.Prepare(Request{
		SessionID: history.Header().ID,
		Events:    history.Events(),
		ToolName:  "read",
		CallID:    &callID,
		Reason:    &reason,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !prepared.Interactive() || history.Seq() != 1 {
		t.Fatal("prepare performed effects or lost policy")
	}
	callID = "changed"
	reason = "changed"
	question := prepared.Question()
	if *question.CallID != "call" || *question.Reason != "permission" {
		t.Fatal("request data was borrowed")
	}
	*question.CallID = "mutated"
	if *prepared.Question().CallID != "call" {
		t.Fatal("question aliases decision")
	}
	asked, err := prepared.Asked()
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := prepared.Resolve(OutcomeAllowedOnce, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if history.Seq() != 1 {
		t.Fatal("decision wrote Session")
	}
	receipt, err := history.Commit(context.Background(), session.Batch(asked, resolved.Draft))
	if err != nil {
		t.Fatal(err)
	}
	var before Asked
	var after Decided
	if err := json.Unmarshal(receipt.Events[0].Data, &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(receipt.Events[1].Data, &after); err != nil {
		t.Fatal(err)
	}
	if before.ID == "" || before.ID != after.ID || after.ID != question.ID || after.Outcome != OutcomeAllowedOnce {
		t.Fatalf("audit mismatch: %#v %#v", before, after)
	}
}

func TestDecisionResolutionFailsClosed(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		policy    Policy
		answer    Outcome
		cancelled bool
		failure   error
		want      Outcome
	}{
		{
			name:      "cancel beats late allow",
			policy:    PolicyAsk,
			answer:    OutcomeAllowedOnce,
			cancelled: true,
			want:      OutcomeCancelled,
		},
		{
			name:   "never rejects",
			policy: PolicyNever,
			answer: OutcomeAllowedOnce,
			want:   OutcomeRejected,
		},
		{
			name:   "invalid answer",
			policy: PolicyAsk,
			answer: "invalid",
			want:   OutcomeUnavailable,
		},
		{
			name:    "failed answer",
			policy:  PolicyAsk,
			answer:  OutcomeAllowedOnce,
			failure: errors.New("unavailable"),
			want:    OutcomeUnavailable,
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			history := openLog(t)
			prepared, err := rulesForTest(t, scenario.policy).Prepare(Request{
				SessionID: history.Header().ID,
				Events:    history.Events(),
				ToolName:  "read",
			})
			if err != nil {
				t.Fatal(err)
			}
			result, err := prepared.Resolve(scenario.answer, scenario.cancelled, scenario.failure)
			if err != nil || result.Outcome != scenario.want {
				t.Fatalf("resolution = %#v, %v", result, err)
			}
		})
	}
	if _, err := new(Decision).Resolve(OutcomeAllowedOnce, false, nil); err == nil {
		t.Fatal("unprepared decision allowed")
	}
	rules := rulesForTest(t, PolicyAsk)
	if _, err := rules.Prepare(Request{
		SessionID: "closed",
		ToolName:  "read",
	}); err == nil {
		t.Fatal("request outside turn accepted")
	}
}

func TestPolicyDraftsPreserveAttributionAndNoChange(t *testing.T) {
	history := openLog(t)
	rules := rulesForTest(t, PolicyAsk)
	if planned, err := rules.ChangePolicy(history.Events(), PolicyAsk); err != nil || planned != nil {
		t.Fatalf("unchanged policy = %#v, %v", planned, err)
	}
	planned, err := rules.ChangePolicy(history.Events(), PolicyNever)
	if err != nil {
		t.Fatal(err)
	}
	if history.Seq() != 1 {
		t.Fatal("policy preparation wrote log")
	}
	if _, err := history.Commit(context.Background(), session.Batch(planned.Draft)); err != nil {
		t.Fatal(err)
	}
	if effective, err := PolicyAsk.Resolve(history.Events()); err != nil || effective != PolicyNever {
		t.Fatalf("policy = %s, %v", effective, err)
	}
	seeded, err := DelegationPolicyDraft()
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := history.Commit(context.Background(), session.Batch(seeded))
	if err != nil {
		t.Fatal(err)
	}
	var payload PolicyChanged
	if err := json.Unmarshal(receipt.Events[0].Data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Policy != PolicyNever || payload.Source == nil || *payload.Source != PolicySourceDelegation {
		t.Fatalf("seed attribution = %#v", payload)
	}
}
