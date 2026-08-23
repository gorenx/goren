package tools_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/gorenx/goren/approval"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/tools"
)

type approvalProviderPlugin struct {
	plugin.Base
	outcome approval.Outcome
	failure error

	mutex       sync.Mutex
	lastRequest approval.Request
}

func (owner *approvalProviderPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "approval-provider",
		Provides: []plugin.ProvidedService{
			plugin.NewProvidedService[approval.Approval](owner),
		},
	}
}

func (*approvalProviderPlugin) Apply(context.Context) error {
	return nil
}

func (*approvalProviderPlugin) Dispose(context.Context) error {
	return nil
}

func (provider *approvalProviderPlugin) Request(
	_ context.Context,
	decisionRequest approval.Request,
) (approval.Outcome, error) {
	provider.mutex.Lock()
	provider.lastRequest = decisionRequest
	provider.mutex.Unlock()
	return provider.outcome, provider.failure
}

func (*approvalProviderPlugin) EffectivePolicy(
	*session.Session,
) (approval.Policy, error) {
	return approval.PolicyAsk, nil
}

func (*approvalProviderPlugin) OverrideOf(
	*session.Session,
) (approval.Policy, bool, error) {
	return "", false, nil
}

func (*approvalProviderPlugin) SetPolicy(
	context.Context,
	approval.ApprovalTarget,
	approval.Policy,
) error {
	return nil
}

type executionSubject struct {
	conversation *session.Session
}

func (subject *executionSubject) SessionValue() *session.Session {
	return subject.conversation
}

func (*executionSubject) Inject(llm.UserMessage) error {
	return nil
}

func newExecutionSubject(t *testing.T) *executionSubject {
	t.Helper()
	conversation, err := session.New(
		"tools-policy",
		session.CreateOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	return &executionSubject{
		conversation: conversation,
	}
}

func askPolicy(reason string) plugin.Plugin {
	return &waterfallPlugin[
		tools.PreExecuteRequest,
		tools.PreExecuteOutcome,
	]{
		name: "ask-policy",
		middleware: waterfallFunc[
			tools.PreExecuteRequest,
			tools.PreExecuteOutcome,
		](func(
			context.Context,
			tools.PreExecuteRequest,
			plugin.WaterfallAction[
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

func TestAskDecisionUsesOptionalApprovalBeforeDispatch(t *testing.T) {
	provider := &approvalProviderPlugin{
		outcome: approval.OutcomeAllowedOnce,
	}
	state := newToolsFixture(
		t,
		provider,
		askPolicy("needs permission"),
	)
	bodyCalls := 0
	if _, err := state.service.AddTool(
		context.Background(),
		objectTool(
			"danger",
			"",
			tools.ExecutorFunc(func(
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
	subject := newExecutionSubject(t)
	outcome := state.service.Execute(
		context.Background(),
		tools.ToolExecutionInput{
			CallID:    "danger-1",
			Name:      "danger",
			Arguments: json.RawMessage(`{}`),
			Subject:   subject,
		},
	)
	if outcome.Failed() || bodyCalls != 1 {
		t.Fatalf("approved outcome = %#v, body calls = %d", outcome, bodyCalls)
	}
	provider.mutex.Lock()
	capturedRequest := provider.lastRequest
	provider.mutex.Unlock()
	if capturedRequest.Subject != subject || capturedRequest.ToolName != "danger" ||
		capturedRequest.CallID == nil || *capturedRequest.CallID != "danger-1" ||
		capturedRequest.Reason == nil ||
		*capturedRequest.Reason != "needs permission" {
		t.Fatalf("approval request = %#v", capturedRequest)
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
			tools.ExecutorFunc(func(
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
				Feedback: []llm.ContentBlock{
					llm.NewTextBlock("blocked"),
				},
			},
			wantFailed:  true,
			wantContent: "blocked",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			policy := &waterfallPlugin[
				tools.PostExecuteRequest,
				tools.PostExecuteOutcome,
			]{
				name: "post-decision",
				middleware: waterfallFunc[
					tools.PostExecuteRequest,
					tools.PostExecuteOutcome,
				](func(
					context.Context,
					tools.PostExecuteRequest,
					plugin.WaterfallAction[
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
					tools.ExecutorFunc(passThroughBody),
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
	policy := &waterfallPlugin[
		tools.PostExecuteRequest,
		tools.PostExecuteOutcome,
	]{
		name: "replace-concluding-value",
		middleware: waterfallFunc[
			tools.PostExecuteRequest,
			tools.PostExecuteOutcome,
		](func(
			context.Context,
			tools.PostExecuteRequest,
			plugin.WaterfallAction[
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
			tools.ExecutorFunc(func(
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
	observer := &eventObserverPlugin{
		name: "failing-result-observer",
		subscriptions: []plugin.EventSubscription{
			plugin.EventOf[tools.ExecutionCompleted](),
		},
		observe: func(context.Context, plugin.Event) error {
			return observerFailure
		},
	}
	state := newToolsFixture(t, observer)
	if _, err := state.service.AddTool(
		context.Background(),
		objectTool(
			"observed",
			"",
			tools.ExecutorFunc(passThroughBody),
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
	failures := append([]plugin.EventFailure(nil), state.failures.failures...)
	state.failures.mutex.Unlock()
	if len(failures) != 1 || failures[0].EventName != tools.ResultEventName ||
		!strings.Contains(failures[0].Error.Error(), observerFailure.Error()) {
		t.Fatalf("reported failures = %#v", failures)
	}
}
