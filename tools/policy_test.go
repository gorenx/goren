package tools_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/approval"
	"github.com/gorenx/goren/tools"
)

func askPolicy(reason string) any {
	return &policyFixture[
		tools.PreExecuteRequest,
		tools.PreExecuteOutcome,
	]{
		name: "ask-policy",
		middleware: policyFunc[
			tools.PreExecuteRequest,
			tools.PreExecuteOutcome,
		](func(
			context.Context,
			tools.PreExecuteRequest,
			action[
				tools.PreExecuteRequest,
				tools.PreExecuteOutcome,
			],
		) (tools.PreExecuteOutcome, error) {
			return tools.PreExecuteOutcome{
				Decision: tools.AskDecision{
					Reason: reason,
				},
			}, nil
		}),
	}
}

func TestPolicyExecutionViewCannotDispatchPreparedCall(t *testing.T) {
	var captured tools.ToolExecution
	observer := &policyFixture[tools.PreExecuteRequest, tools.PreExecuteOutcome]{
		name: "capture-execution",
		middleware: policyFunc[tools.PreExecuteRequest, tools.PreExecuteOutcome](func(
			ctx context.Context,
			input tools.PreExecuteRequest,
			downstream action[tools.PreExecuteRequest, tools.PreExecuteOutcome],
		) (tools.PreExecuteOutcome, error) {
			captured = input.Execution()
			return downstream.Execute(ctx, input)
		}),
	}
	state := newToolsFixture(t, observer)
	bodyCalls := 0
	if _, err := state.service.AddTool(context.Background(), objectTool("read", "", executorStub(func(arguments json.RawMessage, _ tools.ToolRunContext) (json.RawMessage, error) {
		bodyCalls++
		return arguments, nil
	}))); err != nil {
		t.Fatal(err)
	}
	scheduler := state.service.Scheduler()
	prepared, err := scheduler.Prepare(context.Background(), tools.ToolExecutionInput{
		SessionID: "session",
		CallID:    "read-1",
		Name:      "read",
		Arguments: json.RawMessage(`{}`),
	})
	if err != nil || prepared.Stage != tools.ScheduledDispatch {
		t.Fatalf("preparation = %#v, %v", prepared, err)
	}
	if captured.CallID != prepared.Execution.CallID || captured.Name != "read" {
		t.Fatalf("execution view lost call identity: %#v", captured)
	}
	if _, err := scheduler.Dispatch(captured); err == nil || bodyCalls != 0 {
		t.Fatalf("policy view dispatched tool: calls = %d, err = %v", bodyCalls, err)
	}
	dispatched, err := scheduler.Dispatch(prepared.Execution)
	if err != nil || bodyCalls != 1 {
		t.Fatalf("original handle dispatch: calls = %d, err = %v", bodyCalls, err)
	}
	final, err := scheduler.Finalize(prepared.Execution, dispatched.Result)
	if err != nil || final.Failed() {
		t.Fatalf("original handle finalization = %#v, %v", final, err)
	}
}

func TestAskDecisionWaitsForAuditedOutcomeBeforeDispatch(t *testing.T) {
	state := newToolsFixture(t, askPolicy("needs permission"))
	bodyCalls := 0
	if _, err := state.service.AddTool(context.Background(), objectTool("danger", "", executorStub(func(arguments json.RawMessage, _ tools.ToolRunContext) (json.RawMessage, error) {
		bodyCalls++
		return arguments, nil
	}))); err != nil {
		t.Fatal(err)
	}
	scheduler := state.service.Scheduler()
	prepared, err := scheduler.Prepare(context.Background(), tools.ToolExecutionInput{
		SessionID: "session",
		CallID:    "danger-1",
		Name:      "danger",
		Arguments: json.RawMessage(`{}`),
	})
	if err != nil || prepared.Stage != tools.ScheduledApproval || prepared.Approval == nil {
		t.Fatalf("preparation = %#v, %v", prepared, err)
	}
	if prepared.Approval.SessionID != "session" || prepared.Approval.Reason != "needs permission" || bodyCalls != 0 {
		t.Fatal("invalid approval request or early dispatch")
	}
	if _, err := scheduler.Dispatch(prepared.Execution); err == nil {
		t.Fatal("dispatch bypassed approval")
	}
	allowed, err := scheduler.ResolveApproval(prepared.Execution, approval.OutcomeAllowedOnce)
	if err != nil || allowed.Stage != tools.ScheduledDispatch {
		t.Fatalf("allowed = %#v, %v", allowed, err)
	}
	if _, err := scheduler.ResolveApproval(prepared.Execution, approval.OutcomeAllowedOnce); err == nil {
		t.Fatal("duplicate outcome accepted")
	}
	dispatched, err := scheduler.Dispatch(allowed.Execution)
	if err != nil || bodyCalls != 1 {
		t.Fatalf("dispatch calls = %d, err = %v", bodyCalls, err)
	}
	result, err := scheduler.Finalize(allowed.Execution, dispatched.Result)
	if err != nil || result.Failed() {
		t.Fatalf("result = %#v, %v", result, err)
	}
}

func TestAskDecisionFailsClosedWithoutApproval(t *testing.T) {
	state := newToolsFixture(t, askPolicy("needs permission"))
	bodyCalls := 0
	if _, err := state.service.AddTool(
		context.Background(),
		objectTool(
			"danger",
			"",
			executorStub(func(
				arguments json.RawMessage,
				_ tools.ToolRunContext,
			) (json.RawMessage, error) {
				bodyCalls++
				return arguments, nil
			}),
		),
	); err != nil {
		t.Fatal(err)
	}
	outcome := state.service.Execute(
		context.Background(),
		tools.ToolExecutionInput{
			CallID:    "danger-1",
			Name:      "danger",
			Arguments: json.RawMessage(`{}`),
		},
	)
	if !outcome.Failed() || bodyCalls != 0 ||
		resultText(t, outcome) != "Error: needs permission" {
		t.Fatalf("unapproved outcome = %#v, body calls = %d", outcome, bodyCalls)
	}
}

func TestPostPolicyCanReplaceValueOrBlock(t *testing.T) {
	testCases := []struct {
		name        string
		decision    tools.PostToolDecision
		wantFailed  bool
		wantContent string
	}{
		{
			name: "replace value",
			decision: tools.ReplaceValueDecision{
				Value: json.RawMessage(`{"changed":true}`),
			},
			wantContent: `{"changed":true}`,
		},
		{
			name: "block",
			decision: tools.BlockDecision{
				Feedback: []agentmessage.ContentBlock{
					agentmessage.NewTextBlock("blocked"),
				},
			},
			wantFailed:  true,
			wantContent: "blocked",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			policy := &policyFixture[
				tools.PostExecuteRequest,
				tools.PostExecuteOutcome,
			]{
				name: "post-decision",
				middleware: policyFunc[
					tools.PostExecuteRequest,
					tools.PostExecuteOutcome,
				](func(
					context.Context,
					tools.PostExecuteRequest,
					action[
						tools.PostExecuteRequest,
						tools.PostExecuteOutcome,
					],
				) (tools.PostExecuteOutcome, error) {
					return tools.PostExecuteOutcome{
						Decision: testCase.decision,
					}, nil
				}),
			}
			state := newToolsFixture(t, policy)
			if _, err := state.service.AddTool(
				context.Background(),
				objectTool(
					"normalized",
					"",
					executorStub(passThroughBody),
				),
			); err != nil {
				t.Fatal(err)
			}
			outcome := state.service.Execute(
				context.Background(),
				tools.ToolExecutionInput{
					CallID:    "post-1",
					Name:      "normalized",
					Arguments: json.RawMessage(`{"original":true}`),
				},
			)
			if outcome.Failed() != testCase.wantFailed ||
				resultText(t, outcome) != testCase.wantContent {
				t.Fatalf("post outcome = %#v", outcome)
			}
		})
	}
}

func TestPostValueReplacementPreservesTurnConclusion(t *testing.T) {
	policy := &policyFixture[
		tools.PostExecuteRequest,
		tools.PostExecuteOutcome,
	]{
		name: "replace-concluding-value",
		middleware: policyFunc[
			tools.PostExecuteRequest,
			tools.PostExecuteOutcome,
		](func(
			context.Context,
			tools.PostExecuteRequest,
			action[
				tools.PostExecuteRequest,
				tools.PostExecuteOutcome,
			],
		) (tools.PostExecuteOutcome, error) {
			return tools.PostExecuteOutcome{
				Decision: tools.ReplaceValueDecision{
					Value: json.RawMessage(`{"replacement":true}`),
				},
			}, nil
		}),
	}
	state := newToolsFixture(t, policy)
	if _, err := state.service.AddTool(
		context.Background(),
		objectTool(
			"concluding",
			"",
			executorStub(func(
				arguments json.RawMessage,
				runContext tools.ToolRunContext,
			) (json.RawMessage, error) {
				runContext.ConcludeTurn()
				return arguments, nil
			}),
		),
	); err != nil {
		t.Fatal(err)
	}
	outcome := state.service.Execute(
		context.Background(),
		tools.ToolExecutionInput{
			CallID:    "concluding-1",
			Name:      "concluding",
			Arguments: json.RawMessage(`{"original":true}`),
		},
	)
	succeeded, matches := outcome.(*tools.ToolExecutionSuccess)
	if !matches || !succeeded.ConcludesTurn ||
		string(succeeded.Value) != `{"replacement":true}` {
		t.Fatalf("replacement outcome = %#v", outcome)
	}
}

func TestBestEffortResultFailureDoesNotReplaceToolOutcome(t *testing.T) {
	observerFailure := errors.New("observer failed")
	observer := &eventObserver{
		name: "failing-result-observer",
		subscriptions: []string{
			tools.ResultEventName,
		},
		observe: func(context.Context, toolEvent) error {
			return observerFailure
		},
	}
	state := newToolsFixture(t, observer)
	if _, err := state.service.AddTool(
		context.Background(),
		objectTool(
			"observed",
			"",
			executorStub(passThroughBody),
		),
	); err != nil {
		t.Fatal(err)
	}
	outcome := state.service.Execute(
		context.Background(),
		tools.ToolExecutionInput{
			CallID:    "observed-1",
			Name:      "observed",
			Arguments: json.RawMessage(`{}`),
		},
	)
	if outcome.Failed() {
		t.Fatalf("observer replaced Tool outcome = %#v", outcome)
	}
	state.failures.mutex.Lock()
	failures := append([]observationFailure(nil), state.failures.failures...)
	state.failures.mutex.Unlock()
	if len(failures) != 1 || failures[0].EventName != tools.ResultEventName ||
		!strings.Contains(failures[0].Error.Error(), observerFailure.Error()) {
		t.Fatalf("reported failures = %#v", failures)
	}
}

func TestApprovalRejectionAndCancellationNeverDispatch(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		outcome approval.Outcome
		cancel  bool
	}{
		{
			name:    "rejected",
			outcome: approval.OutcomeRejected,
		},
		{
			name:    "unavailable",
			outcome: approval.OutcomeUnavailable,
		},
		{
			name:    "cancelled answer",
			outcome: approval.OutcomeCancelled,
		},
		{
			name:    "cancelled before late allow",
			outcome: approval.OutcomeAllowedOnce,
			cancel:  true,
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			state := newToolsFixture(t, askPolicy("permission"))
			calls := 0
			if _, err := state.service.AddTool(context.Background(), objectTool("danger", "", executorStub(func(raw json.RawMessage, _ tools.ToolRunContext) (json.RawMessage, error) {
				calls++
				return raw, nil
			}))); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			scheduler := state.service.Scheduler()
			prepared, err := scheduler.Prepare(ctx, tools.ToolExecutionInput{
				SessionID: "session",
				CallID:    "call",
				Name:      "danger",
				Arguments: json.RawMessage(`{}`),
			})
			if err != nil || prepared.Stage != tools.ScheduledApproval {
				t.Fatalf("prepare = %#v, %v", prepared, err)
			}
			if scenario.cancel {
				cancel()
			}
			resumed, err := scheduler.ResolveApproval(prepared.Execution, scenario.outcome)
			if err != nil || resumed.Stage != tools.ScheduledPostResult {
				t.Fatalf("resume = %#v, %v", resumed, err)
			}
			if _, err := scheduler.Dispatch(resumed.Execution); err == nil {
				t.Fatal("denied execution dispatched")
			}
			result, err := scheduler.Finalize(resumed.Execution, resumed.Result)
			if err != nil || !result.Failed() || calls != 0 {
				t.Fatalf("result = %#v, calls = %d, err = %v", result, calls, err)
			}
		})
	}
}
