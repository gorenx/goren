package approval_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/approval"
	"github.com/gorenx/goren/session"
)

type fakeSubject struct {
	conversation session.Context
	injected     []agentmessage.UserMessage
}

func (subject *fakeSubject) SessionValue() session.Context { return subject.conversation }
func (subject *fakeSubject) Inject(input agentmessage.UserMessage) error {
	subject.injected = append(subject.injected, input)
	return nil
}

func newService(t *testing.T, settings approval.Config, responder approval.Answerer) *approval.Service {
	t.Helper()
	validated, err := approval.ValidateConfig(settings)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := approval.New(validated, responder)
	if err != nil {
		t.Fatal(err)
	}
	return owner
}

type responder struct {
	calls      int
	input      approval.Question
	outcome    approval.Outcome
	err        error
	panicValue bool
	started    chan struct{}
}

func (endpoint *responder) Answer(ctx context.Context, input approval.Question) (approval.Outcome, error) {
	endpoint.calls++
	endpoint.input = input
	if endpoint.panicValue {
		panic("answerer failed")
	}
	if endpoint.started != nil {
		close(endpoint.started)
		<-ctx.Done()
	}
	return endpoint.outcome, endpoint.err
}

func newSubject(
	testingContext *testing.T,
	identifier session.SessionID,
) *fakeSubject {
	testingContext.Helper()
	conversation, err := session.New(identifier, session.CreateOptions{})
	if err != nil {
		testingContext.Fatal(err)
	}
	return &fakeSubject{
		conversation: conversation,
	}
}

func openTurn(testingContext *testing.T, conversation session.Context) {
	testingContext.Helper()
	{
		draft, err := session.NewEventDraft(session.TurnStarted,
			session.TurnStart{
				Turn: 1,
			})
		if err == nil {
			_, err = conversation.Commit(context.Background(), session.Batch(draft))
		}
		if err != nil {
			testingContext.Fatal(err)
		}
	}
}

func TestRequestRequiresOpenTurnAndPairsAudit(t *testing.T) {
	t.Parallel()
	owner := newService(t, approval.Config{}, nil)
	subject := newSubject(t, "approval-audit")

	if _, err := owner.Request(
		context.Background(),
		approval.Request{
			Subject:  subject,
			ToolName: "echo",
		},
	); err == nil || !strings.Contains(err.Error(), "outside an open turn") {
		t.Fatalf("idle request error = %v", err)
	}
	if len(subject.conversation.Events()) != 0 {
		t.Fatal("idle request appended an audit event")
	}

	openTurn(t, subject.conversation)
	callIdentifier := agentmessage.CallID("call-1")
	reasonText := "hook says ask"
	outcome, err := owner.Request(
		context.Background(),
		approval.Request{
			Subject:  subject,
			ToolName: "echo",
			CallID:   &callIdentifier,
			Reason:   &reasonText,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if outcome != approval.OutcomeUnavailable {
		t.Fatalf("outcome = %q", outcome)
	}
	entries := subject.conversation.Events()
	actualTypes := []string{
		entries[1].Type,
		entries[2].Type,
	}
	wantTypes := []string{
		approval.AskedEventName,
		approval.DecidedEventName,
	}
	if !reflect.DeepEqual(actualTypes, wantTypes) {
		t.Fatalf("audit event types = %#v", actualTypes)
	}
	var asked approval.Asked
	var decided approval.Decided
	if err := json.Unmarshal(entries[1].Data, &asked); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(entries[2].Data, &decided); err != nil {
		t.Fatal(err)
	}
	if asked.ID == "" || asked.ToolName != "echo" || asked.CallID == nil ||
		*asked.CallID != callIdentifier || asked.Reason == nil ||
		*asked.Reason != reasonText || decided.ID != asked.ID ||
		decided.Outcome != outcome {
		t.Fatalf("audit pair = (%#v, %#v)", asked, decided)
	}
}

func TestAnswererReceivesAuditedIdentityAndContainsFailure(t *testing.T) {
	cases := []struct {
		name       string
		outcome    approval.Outcome
		err        error
		panicValue bool
		want       approval.Outcome
	}{
		{
			name:    "allowed",
			outcome: approval.OutcomeAllowedOnce,
			want:    approval.OutcomeAllowedOnce,
		},
		{
			name:    "invalid",
			outcome: "rogue",
			want:    approval.OutcomeUnavailable,
		},
		{
			name: "error",
			err:  errors.New("answer failed"),
			want: approval.OutcomeUnavailable,
		},
		{
			name:       "panic",
			panicValue: true,
			want:       approval.OutcomeUnavailable,
		},
	}
	for _, scenario := range cases {
		t.Run(scenario.name, func(t *testing.T) {
			endpoint := &responder{
				outcome:    scenario.outcome,
				err:        scenario.err,
				panicValue: scenario.panicValue,
			}
			owner := newService(t, approval.Config{}, endpoint)
			subject := newSubject(t, "approval-answer")
			openTurn(t, subject.conversation)
			outcome, err := owner.Request(context.Background(), approval.Request{
				Subject:  subject,
				ToolName: "echo",
			})
			if err != nil || outcome != scenario.want {
				t.Fatalf("outcome = %q, %v", outcome, err)
			}
			entries := subject.conversation.Events()
			var asked approval.Asked
			var decided approval.Decided
			if err := json.Unmarshal(entries[1].Data, &asked); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(entries[2].Data, &decided); err != nil {
				t.Fatal(err)
			}
			if endpoint.input.ID != asked.ID || endpoint.input.SessionID != subject.conversation.ID() || endpoint.input.ToolName != "echo" || decided.ID != asked.ID || decided.Outcome != scenario.want {
				t.Fatalf("request correlation lost: %+v / %+v / %+v", endpoint.input, asked, decided)
			}
		})
	}
}

func TestPolicyFoldControlsDispatchAndText(t *testing.T) {
	endpoint := &responder{
		outcome: approval.OutcomeAllowedOnce,
	}
	owner := newService(t, approval.Config{}, endpoint)
	subject := newSubject(t, "approval-policy")
	openTurn(t, subject.conversation)
	for range 2 {
		if err := owner.SetPolicy(context.Background(), subject, approval.PolicyNever); err != nil {
			t.Fatal(err)
		}
	}
	outcome, err := owner.Request(context.Background(), approval.Request{
		Subject:  subject,
		ToolName: "echo",
	})
	if err != nil || outcome != approval.OutcomeRejected || endpoint.calls != 0 {
		t.Fatalf("never outcome = %q, %v, calls %d", outcome, err, endpoint.calls)
	}
	if len(subject.injected) != 1 {
		t.Fatal("duplicate policy notice")
	}
	selectedPolicy, err := owner.EffectivePolicy(subject.conversation)
	if err != nil {
		t.Fatal(err)
	}
	textValue, err := selectedPolicy.Text()
	if err != nil || !strings.Contains(textValue, "Approval prompts are disabled in this session") {
		t.Fatalf("text = %q, %v", textValue, err)
	}
	child := newSubject(t, "delegated")
	if err := owner.SeedDelegationPolicy(context.Background(), child.conversation); err != nil {
		t.Fatal(err)
	}
	var change approval.PolicyChanged
	if err := json.Unmarshal(child.conversation.Events()[0].Data, &change); err != nil {
		t.Fatal(err)
	}
	if change.Policy != approval.PolicyNever || change.Source == nil || *change.Source != approval.PolicySourceDelegation {
		t.Fatalf("delegation policy = %+v", change)
	}
}

func TestRequestCancellationWinsAndAppendsOneDecision(t *testing.T) {
	started := make(chan struct{})
	endpoint := &responder{
		started: started,
		outcome: approval.OutcomeAllowedOnce,
	}
	owner := newService(t, approval.Config{}, endpoint)
	subject := newSubject(t, "approval-cancel")
	openTurn(t, subject.conversation)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		outcome approval.Outcome
		err     error
	}
	settled := make(chan result, 1)
	go func() {
		outcome, err := owner.Request(ctx, approval.Request{
			Subject:  subject,
			ToolName: "echo",
		})
		settled <- result{
			outcome: outcome,
			err:     err,
		}
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("answerer did not start")
	}
	cancel()
	select {
	case completed := <-settled:
		if completed.err != nil || completed.outcome != approval.OutcomeCancelled {
			t.Fatalf("cancel result = %+v", completed)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not settle")
	}
	decisions := 0
	for _, entry := range subject.conversation.Events() {
		if entry.Type == approval.DecidedEventName {
			decisions++
			var decided approval.Decided
			if err := json.Unmarshal(entry.Data, &decided); err != nil {
				t.Fatal(err)
			}
			if decided.Outcome != approval.OutcomeCancelled {
				t.Fatalf("late allow won: %+v", decided)
			}
		}
	}
	if decisions != 1 {
		t.Fatalf("decided events = %d", decisions)
	}
}

func TestConfigDecodingRejectsInvalidInput(t *testing.T) {
	for _, raw := range []string{`{"unknown":true}`, `{"policy":null}`, `{"policy":"sometimes"}`, `[]`} {
		var settings approval.Config
		err := json.Unmarshal([]byte(raw), &settings)
		if err == nil {
			_, err = approval.ValidateConfig(settings)
		}
		if err == nil {
			t.Fatalf("invalid config accepted: %s", raw)
		}
	}
	if _, err := approval.New(approval.ValidatedConfig{}, nil); err == nil {
		t.Fatal("unvalidated configuration accepted")
	}
}
