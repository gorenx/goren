package llmretry

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
)

func requestFailure(policy llm.RetryPolicy) agent.RequestFailure {
	return agent.RequestFailure{
		AgentID:  "retry-fixture",
		Turn:     1,
		Step:     1,
		Provider: "mock",
		Failure: llm.LlmFailure{
			Message: "busy",
			Code:    "SERVER",
		},
		RetryPolicy: policy,
	}
}
func normalPolicy() llm.NormalRetryPolicy {
	return llm.NormalRetryPolicy{
		Mode:           llm.RetryNormal,
		MaxRetries:     2,
		RetryableCodes: []string{"SERVER"},
		ResolvedRetryBackoff: llm.ResolvedRetryBackoff{
			InitialDelayMS: 10,
			MaxDelayMS:     100,
			JitterRatio:    0,
		},
	}
}

func runRecovery(t *testing.T, history *session.Log, notice agent.RequestFailure, downstream Continued) (Completed, int, []float64) {
	t.Helper()
	current, err := NewRecovery(notice, .5, "chain")
	if err != nil {
		t.Fatal(err)
	}
	next, err := current.Start()
	if err != nil {
		t.Fatal(err)
	}
	delegated := 0
	var delays []float64
	for index := 0; index < 10; index++ {
		var result Result
		switch effect := next.(type) {
		case ReadHistory:
			result = HistoryRead{
				Snapshot: history.Snapshot(),
			}
		case Continue:
			delegated++
			result = downstream
		case Commit:
			write, err := history.Commit(context.Background(), effect.Plan)
			if err != nil {
				t.Fatal(err)
			}
			replay, err := history.Commit(context.Background(), effect.Plan)
			if err != nil || !reflect.DeepEqual(write, replay) {
				t.Fatalf("idempotent retry fact: %v, %v", replay, err)
			}
			result = Committed{
				Write: write,
			}
		case Wait:
			delays = append(delays, effect.Milliseconds)
			result = Waited{}
		case Completed:
			return effect, delegated, delays
		default:
			t.Fatalf("unexpected effect %T", effect)
		}
		next, err = current.Advance(result)
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Fatal("recovery did not complete")
	return Completed{}, 0, nil
}

func TestRecoveryBudgetAndIdempotentFacts(t *testing.T) {
	history := openRetrySession(t, "mock")
	baseline := history.Seq()
	for number := 1; number <= 3; number++ {
		result, delegated, delays := runRecovery(t, history, requestFailure(normalPolicy()), Continued{})
		if result.Err != nil {
			t.Fatal(result.Err)
		}
		if number <= 2 {
			if !result.Action.Retry || delegated != 0 || len(delays) != 1 || delays[0] != float64(10*number) {
				t.Fatalf("attempt %d: %+v %d %v", number, result, delegated, delays)
			}
		} else if result.Action.Retry || delegated != 1 || len(delays) != 0 {
			t.Fatalf("exhausted: %+v %d %v", result, delegated, delays)
		}
	}
	if history.Seq() != baseline+4 {
		t.Fatalf("duplicate or missing facts: %d", history.Seq()-baseline)
	}
	if err := ValidateHistory(history.Events()); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryDelegationAndAlwaysPrecedence(t *testing.T) {
	for _, scenario := range []string{"no policy", "non transient", "retry after cap", "always accepted", "always declined", "always failed"} {
		t.Run(scenario, func(t *testing.T) {
			history := openRetrySession(t, "mock")
			notice := requestFailure(normalPolicy())
			downstream := Continued{}
			switch scenario {
			case "no policy":
				notice.RetryPolicy = nil
			case "non transient":
				notice.Failure.Code = "INVALID_ARGS"
			case "retry after cap":
				value := 101.0
				notice.Failure.ProviderRetryAfterMS = &value
			default:
				notice.RetryPolicy = llm.AlwaysRetryPolicy{
					Mode:                 llm.RetryAlways,
					ResolvedRetryBackoff: normalPolicy().ResolvedRetryBackoff,
				}
				if scenario == "always accepted" {
					downstream.Action.Retry = true
				}
				if scenario == "always failed" {
					downstream.Err = errors.New("downstream failed")
				}
			}
			result, delegated, delays := runRecovery(t, history, notice, downstream)
			if result.Err != nil || delegated != 1 {
				t.Fatalf("delegation: %+v %d", result, delegated)
			}
			scheduled := scenario == "always declined" || scenario == "always failed"
			if (len(delays) == 1) != scheduled {
				t.Fatalf("waits: %v", delays)
			}
			if result.Action.Retry != (scheduled || scenario == "always accepted") {
				t.Fatalf("decision: %+v", result)
			}
		})
	}
}

func TestRecoveryCancelWaitPreservesScheduleWithoutStarting(t *testing.T) {
	history := openRetrySession(t, "mock")
	current, err := NewRecovery(requestFailure(normalPolicy()), .5, "chain")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := current.Start(); err != nil {
		t.Fatal(err)
	}
	next, err := current.Advance(HistoryRead{
		Snapshot: history.Snapshot(),
	})
	if err != nil {
		t.Fatal(err)
	}
	write, err := history.Commit(context.Background(), next.(Commit).Plan)
	if err != nil {
		t.Fatal(err)
	}
	next, err = current.Advance(Committed{
		Write: write,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := next.(Wait); !ok {
		t.Fatalf("wait: %T", next)
	}
	canceled := errors.New("parent canceled")
	current.Cancel(canceled)
	next, err = current.Advance(Waited{})
	if err != nil {
		t.Fatal(err)
	}
	result := next.(Completed)
	if result.Action.Retry || !errors.Is(result.Err, canceled) {
		t.Fatalf("cancellation: %+v", result)
	}
	entries := history.Events()
	if entries[len(entries)-1].Type != RetryEventName {
		t.Fatal("canceled wait started retry")
	}
}
