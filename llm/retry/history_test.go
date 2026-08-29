package llmretry

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
)

func TestValidateHistoryRestoresOneRetryChain(t *testing.T) {
	t.Parallel()
	conversation := openRetrySession(t, "mock")
	first := NormalRetryRecord{
		RetryID: "chain-1", Turn: 1, Step: 1, Provider: "mock", Mode: llm.RetryNormal,
		PolicyKey: "policy", Retry: 1, MaxRetries: 2, DelayMS: 10,
		Failure: llm.LlmFailure{Message: "busy", Code: "SERVER"},
	}
	{
		draft, err := session.NewEventDraft(retryScheduledEvent, RetryRecord(first))
		if err == nil {
			_, err = conversation.Commit(context.Background(), session.Batch(draft))
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	{
		draft, err := session.NewEventDraft(retryStartedEvent, RetryStarted{
			RetryID: "chain-1", Turn: 1, Step: 1, Retry: 1,
		})
		if err == nil {
			_, err = conversation.Commit(context.Background(), session.Batch(draft))
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	second := first
	second.Retry = 2
	{
		draft, err := session.NewEventDraft(retryScheduledEvent, RetryRecord(second))
		if err == nil {
			_, err = conversation.Commit(context.Background(), session.Batch(draft))
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := ValidateHistory(conversation.Events()); err != nil {
		t.Fatal(err)
	}
	projection, err := analyzeHistory(conversation.Events())
	if err != nil {
		t.Fatal(err)
	}
	latest, found := projection.priorRetry(retryChainKey{
		turn: 1, step: 1, provider: "mock", policyKey: "policy",
	})
	if !found {
		t.Fatal("restored chain is missing")
	}
	facts, err := factsFromRecord(latest)
	if err != nil {
		t.Fatal(err)
	}
	if facts.retry != 2 || facts.chainID != "chain-1" {
		t.Fatalf("restored facts = %#v", facts)
	}
}

func TestRetryHistoryRejectsBrokenDurableRelationships(t *testing.T) {
	t.Parallel()
	base := openRetrySession(t, "mock").Events()
	valid := NormalRetryRecord{
		RetryID: "chain-1", Turn: 1, Step: 1, Provider: "mock", Mode: llm.RetryNormal,
		PolicyKey: "policy", Retry: 1, MaxRetries: 2, DelayMS: 0,
		Failure: llm.LlmFailure{Message: "busy", Code: "SERVER"},
	}
	validRaw := mustJSON(t, valid)
	validStarted := mustJSON(t, RetryStarted{RetryID: "chain-1", Turn: 1, Step: 1, Retry: 1})
	for _, testCase := range []struct {
		label       string
		events      []session.Event
		wantMessage string
	}{
		{
			label: "wrong provider",
			events: appendEvent(base, RetryEventName, mustJSON(t, NormalRetryRecord{
				RetryID: "chain-1", Turn: 1, Step: 1, Provider: "other", Mode: llm.RetryNormal,
				PolicyKey: "policy", Retry: 1, MaxRetries: 2, DelayMS: 0,
				Failure: llm.LlmFailure{Message: "busy", Code: "SERVER"},
			})),
			wantMessage: "does not match",
		},
		{
			label:       "started without schedule",
			events:      appendEvent(base, RetryStartedEventName, validStarted),
			wantMessage: "pairs no prior",
		},
		{
			label:       "duplicate started",
			events:      appendEvent(appendEvent(appendEvent(base, RetryEventName, validRaw), RetryStartedEventName, validStarted), RetryStartedEventName, validStarted),
			wantMessage: "repeats",
		},
		{
			label: "wrong retry number",
			events: appendEvent(base, RetryEventName, mustJSON(t, NormalRetryRecord{
				RetryID: "chain-1", Turn: 1, Step: 1, Provider: "mock", Mode: llm.RetryNormal,
				PolicyKey: "policy", Retry: 2, MaxRetries: 2, DelayMS: 0,
				Failure: llm.LlmFailure{Message: "busy", Code: "SERVER"},
			})),
			wantMessage: "must equal provider policy retry 1",
		},
	} {
		testCase := testCase
		t.Run(testCase.label, func(t *testing.T) {
			t.Parallel()
			err := ValidateHistory(testCase.events)
			if err == nil || !strings.Contains(err.Error(), testCase.wantMessage) {
				t.Fatalf("error = %v, want containing %q", err, testCase.wantMessage)
			}
		})
	}
}

func TestDecodeRetryRecordPreservesUnionAndRejectsMalformedFailure(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		input       string
		wantMessage string
	}{
		{
			input:       `{"retryId":"x","turn":1,"step":1,"provider":"p","mode":"always","policyKey":"k","retry":1,"maxRetries":2,"delayMs":0,"failure":{"message":"m","code":"c"}}`,
			wantMessage: "must omit maxRetries",
		},
		{
			input:       `{"retryId":"x","turn":1,"step":1,"provider":"p","mode":"normal","policyKey":"k","retry":1,"maxRetries":2,"delayMs":0,"failure":{"message":"m","code":"c","requestId":""}}`,
			wantMessage: "requestId must be a non-empty",
		},
		{
			input:       `{"retryId":"x","turn":1,"step":1,"provider":"p","mode":"normal","policyKey":"k","retry":1,"maxRetries":0,"delayMs":0,"failure":{"message":"m","code":"c"}}`,
			wantMessage: "positive safe maxRetries",
		},
	} {
		if _, err := DecodeRetryRecord(json.RawMessage(testCase.input)); err == nil || !strings.Contains(err.Error(), testCase.wantMessage) {
			t.Fatalf("DecodeRetryRecord(%s) error = %v, want containing %q", testCase.input, err, testCase.wantMessage)
		}
	}
	decoded, err := DecodeRetryRecord(json.RawMessage(
		`{"retryId":"x","turn":1,"step":1,"provider":"p","mode":"always","policyKey":"k","retry":1,"delayMs":0,"failure":{"message":"m","code":"c"},"future":true}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := decoded.(AlwaysRetryRecord); !ok {
		t.Fatalf("decoded type = %T", decoded)
	}
}

func openRetrySession(t *testing.T, providerRoute string) session.Context {
	t.Helper()
	conversation, err := session.New(session.SessionID("retry-fixture"), session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	{
		draft, err := session.NewEventDraft(session.TurnStarted, session.TurnStart{Turn: 1})
		if err == nil {
			_, err = conversation.Commit(context.Background(), session.Batch(draft))
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	{
		draft, err := session.NewEventDraft(session.StepStarted, session.StepPosition{Turn: 1, Step: 1})
		if err == nil {
			_, err = conversation.Commit(context.Background(), session.Batch(draft))
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	{
		draft, err := session.NewEventDraft(session.RequestHeaderSet, session.RequestHeaderSnapshot{
			Header: session.EpochHeader{Config: llm.CallConfig{Provider: providerRoute, Model: "model"}},
			Reason: session.RequestHeaderInitial,
		})
		if err == nil {
			_, err = conversation.Commit(context.Background(), session.Batch(draft))
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	return conversation
}

func mustJSON[T any](t *testing.T, value T) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func appendEvent(events []session.Event, eventType string, data json.RawMessage) []session.Event {
	detached := append([]session.Event(nil), events...)
	detached = append(detached, session.Event{
		Type: eventType, Seq: int64(len(detached)), Time: 0, Data: append(json.RawMessage(nil), data...),
	})
	return detached
}
