package commands

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
)

type commandAgentFixture struct {
	plugin.Base
	conversation session.Context
}

func (subject *commandAgentFixture) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "commands-test-agent",
		Provides: []plugin.ProvidedService{
			plugin.NewProvidedService[agent.Agent](subject),
		},
	}
}

func (*commandAgentFixture) Apply(requestContext context.Context) error {
	return requestContext.Err()
}

func (*commandAgentFixture) Dispose(context.Context) error { return nil }

func (subject *commandAgentFixture) ID() session.SessionID {
	return subject.conversation.ID()
}

func (*commandAgentFixture) OptionsValue() agent.Options { return agent.Options{} }

func (subject *commandAgentFixture) SessionValue() session.Context {
	return subject.conversation
}

func (*commandAgentFixture) InboxValue() *agent.Inbox { return nil }

func (*commandAgentFixture) StatusValue() agent.Status { return agent.StatusIdle }

func (*commandAgentFixture) Cancel(agent.CancelCause, agent.CancelOptions) {}

func (*commandAgentFixture) WhenIdle(context.Context) error { return nil }

func (*commandAgentFixture) RunMaintenance(
	requestContext context.Context,
	operation func(context.Context) error,
) error {
	return operation(requestContext)
}

func (*commandAgentFixture) Send(llm.UserMessage, agent.InboxTarget, bool) error { return nil }

func (*commandAgentFixture) Followup(llm.UserMessage) error { return nil }

func (*commandAgentFixture) Steer(llm.UserMessage) error { return nil }

func (*commandAgentFixture) Inject(llm.UserMessage) error { return nil }

func TestParseCommandPreservesExactTrailingInput(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		line       string
		want       ParsedLine
		wantParsed bool
	}{
		{
			line: "/compact",
			want: ParsedLine{
				Name: "compact",
			},
			wantParsed: true,
		},
		{
			line: "/compact  now ",
			want: ParsedLine{
				Name:     "compact",
				RawInput: "  now ",
			},
			wantParsed: true,
		},
		{
			line: "/a_b-2\nnext",
			want: ParsedLine{
				Name:     "a_b-2",
				RawInput: "\nnext",
			},
			wantParsed: true,
		},
		{
			line: "compact",
		},
		{
			line: "/Compact",
		},
		{
			line: "/compact/value",
		},
		{
			line: " /compact",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.line, func(t *testing.T) {
			parsedValue, matched := ParseCommand(testCase.line)
			if matched != testCase.wantParsed || !reflect.DeepEqual(parsedValue, testCase.want) {
				t.Fatalf(
					"ParseCommand(%q) = (%#v, %t), want (%#v, %t)",
					testCase.line,
					parsedValue,
					matched,
					testCase.want,
					testCase.wantParsed,
				)
			}
		})
	}
}

func TestCommandRuntimeExecutesKnownCommandAsLogOnlyLifecycle(t *testing.T) {
	owner := newCommandRuntimeFixture(t)
	subject := newCommandAgentFixture(t)
	var received Invocation
	handle, err := owner.Register(Definition{
		Name:        "echo",
		Description: "Echo exact input",
		Handler: func(_ context.Context, input Invocation) (Result, error) {
			received = input
			message := input.RawInput
			return Result{
				Kind: ResultSuccess,
				Text: &message,
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(handle.Unregister)

	settled, err := owner.Execute(
		context.Background(),
		subject,
		"/echo  unchanged ",
		ExecuteOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if settled == nil || settled.CommandID != "cmd-fixture-1" ||
		settled.Result.Text == nil || *settled.Result.Text != "  unchanged " {
		t.Fatalf("execution = %#v", settled)
	}
	if received.Agent != subject || received.CommandID != settled.CommandID ||
		received.RawInput != "  unchanged " {
		t.Fatalf("handler invocation = %#v", received)
	}
	entries := subject.conversation.Events()
	if len(entries) != 2 || entries[0].Type != RunEventName || entries[1].Type != DoneEventName {
		t.Fatalf("lifecycle events = %#v", entries)
	}
	var runValue Run
	if err := json.Unmarshal(entries[0].Data, &runValue); err != nil {
		t.Fatal(err)
	}
	var doneValue Done
	if err := json.Unmarshal(entries[1].Data, &doneValue); err != nil {
		t.Fatal(err)
	}
	if runValue.CommandID != settled.CommandID || runValue.Args == nil ||
		*runValue.Args != "  unchanged " || runValue.Source.Kind != "user" ||
		doneValue.CommandID != settled.CommandID || doneValue.Kind != ResultSuccess {
		t.Fatalf("decoded lifecycle = (%#v, %#v)", runValue, doneValue)
	}
	if len(subject.conversation.Surface().Nodes) != 0 {
		t.Fatalf("command lifecycle joined model Surface: %#v", subject.conversation.Surface())
	}
	derived, err := subject.conversation.DeriveMessages()
	if err != nil {
		t.Fatal(err)
	}
	if len(derived) != 0 {
		t.Fatalf("command lifecycle joined model messages: %#v", derived)
	}
}

func TestCommandRuntimeMissesAndImageRejectionDoNotInvokeHandler(t *testing.T) {
	owner := newCommandRuntimeFixture(t)
	subject := newCommandAgentFixture(t)
	called := false
	_, err := owner.Register(Definition{
		Name:        "compact",
		Description: "Compact history",
		Handler: func(context.Context, Invocation) (Result, error) {
			called = true
			return successFixture("unexpected"), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"compact", "/missing"} {
		settled, executeErr := owner.Execute(
			context.Background(),
			subject,
			line,
			ExecuteOptions{},
		)
		if executeErr != nil || settled != nil {
			t.Fatalf("Execute(%q) = (%#v, %v)", line, settled, executeErr)
		}
	}
	settled, err := owner.Execute(
		context.Background(),
		subject,
		"/compact",
		ExecuteOptions{
			AttachmentCount: 1,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if settled == nil || settled.Result.Kind != ResultError ||
		settled.Result.Text == nil || *settled.Result.Text != "/compact does not accept image attachments" {
		t.Fatalf("image rejection = %#v", settled)
	}
	if called {
		t.Fatal("image-rejected command entered handler")
	}
	if len(subject.conversation.Events()) != 2 {
		t.Fatalf("image rejection lifecycle = %#v", subject.conversation.Events())
	}
}

func TestRegistrationWithdrawsThenDrainsCancelledHandler(t *testing.T) {
	owner := newCommandRuntimeFixture(t)
	subject := newCommandAgentFixture(t)
	started := make(chan struct{})
	release := make(chan struct{})
	handle, err := owner.Register(Definition{
		Name:        "wait",
		Description: "Wait for cleanup",
		Handler: func(context.Context, Invocation) (Result, error) {
			close(started)
			<-release
			return successFixture("closed"), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	requestContext, cancelRequest := context.WithCancel(context.Background())
	requestDone := make(chan error, 1)
	go func() {
		_, executeErr := owner.Execute(
			requestContext,
			subject,
			"/wait",
			ExecuteOptions{},
		)
		requestDone <- executeErr
	}()
	<-started
	cancelRequest()
	if executeErr := <-requestDone; !errors.Is(executeErr, context.Canceled) {
		t.Fatalf("cancelled Execute error = %v", executeErr)
	}
	handle.Unregister()
	if _, found := owner.Find(subject, "wait"); found {
		t.Fatal("unregistered command remains discoverable")
	}
	waitContext, cancelWait := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelWait()
	if waitErr := handle.Wait(waitContext); !errors.Is(waitErr, context.DeadlineExceeded) {
		t.Fatalf("Wait before cleanup = %v", waitErr)
	}
	close(release)
	if waitErr := handle.Wait(context.Background()); waitErr != nil {
		t.Fatal(waitErr)
	}
	entries := subject.conversation.Events()
	if len(entries) != 2 || entries[1].Type != DoneEventName {
		t.Fatalf("cancelled lifecycle = %#v", entries)
	}
	var doneValue Done
	if err := json.Unmarshal(entries[1].Data, &doneValue); err != nil {
		t.Fatal(err)
	}
	if doneValue.Kind != ResultError || doneValue.Text == nil ||
		*doneValue.Text != context.Canceled.Error() {
		t.Fatalf("cancelled command/done = %#v", doneValue)
	}
}

func TestCommandRuntimeListsDetachedSortedDescriptors(t *testing.T) {
	owner := newCommandRuntimeFixture(t)
	subject := newCommandAgentFixture(t)
	for _, nameValue := range []string{"zeta", "alpha", "middle"} {
		inputHint := &InputDescriptor{
			Hint: "value",
		}
		if _, err := owner.Register(Definition{
			Name:        nameValue,
			Description: nameValue + " description",
			Input:       inputHint,
			Handler: func(context.Context, Invocation) (Result, error) {
				return successFixture("ok"), nil
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	descriptors := owner.List(subject)
	if names := []string{descriptors[0].Name, descriptors[1].Name, descriptors[2].Name}; !reflect.DeepEqual(names, []string{"alpha", "middle", "zeta"}) {
		t.Fatalf("descriptor names = %#v", names)
	}
	descriptors[0].Input.Hint = "mutated"
	refreshed := owner.List(subject)
	if refreshed[0].Input.Hint != "value" {
		t.Fatalf("descriptor mutation escaped: %#v", refreshed[0])
	}
}

func TestCommandRuntimeHonorsRecordInputAndPreInvocationCancellation(t *testing.T) {
	owner := newCommandRuntimeFixture(t)
	subject := newCommandAgentFixture(t)
	called := false
	recordInput := false
	if _, err := owner.Register(Definition{
		Name:        "private",
		Description: "Do not duplicate domain input",
		RecordInput: &recordInput,
		Handler: func(context.Context, Invocation) (Result, error) {
			called = true
			return successFixture("done"), nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	cancelledContext, cancelRequest := context.WithCancel(context.Background())
	cancelRequest()
	settled, err := owner.Execute(
		cancelledContext,
		subject,
		"/private secret",
		ExecuteOptions{},
	)
	if !errors.Is(err, context.Canceled) || settled != nil || called ||
		len(subject.conversation.Events()) != 0 {
		t.Fatalf(
			"pre-cancelled Execute = (%#v, %v, called=%t, events=%#v)",
			settled,
			err,
			called,
			subject.conversation.Events(),
		)
	}
	settled, err = owner.Execute(
		context.Background(),
		subject,
		"/private secret",
		ExecuteOptions{},
	)
	if err != nil || settled == nil || !called {
		t.Fatalf("recordInput=false Execute = (%#v, %v, called=%t)", settled, err, called)
	}
	var runValue Run
	if err := json.Unmarshal(subject.conversation.Events()[0].Data, &runValue); err != nil {
		t.Fatal(err)
	}
	if runValue.Args != nil {
		t.Fatalf("command/run retained private input: %#v", runValue)
	}
}

func TestCommandRuntimeContainsHandlerFailureAndRejectsInvalidResults(t *testing.T) {
	testCases := []struct {
		name        string
		handler     Handler
		wantMessage string
	}{
		{
			name: "panic",
			handler: func(context.Context, Invocation) (Result, error) {
				panic("handler exploded")
			},
			wantMessage: "commands: handler panic: handler exploded",
		},
		{
			name: "unsafe source sequence",
			handler: func(context.Context, Invocation) (Result, error) {
				unsafeSequence := maxSafeInteger + 1
				return Result{
					Kind:           ResultSuccess,
					SourceEventSeq: &unsafeSequence,
				}, nil
			},
			wantMessage: "commands: success sourceEventSeq must be a non-negative safe integer",
		},
		{
			name: "missing error text",
			handler: func(context.Context, Invocation) (Result, error) {
				return Result{
					Kind: ResultError,
				}, nil
			},
			wantMessage: "commands: error result text must not be empty",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			owner := newCommandRuntimeFixture(t)
			subject := newCommandAgentFixture(t)
			if _, err := owner.Register(Definition{
				Name:        "invalid",
				Description: "Return an invalid result",
				Handler:     testCase.handler,
			}); err != nil {
				t.Fatal(err)
			}
			settled, err := owner.Execute(
				context.Background(),
				subject,
				"/invalid",
				ExecuteOptions{},
			)
			if err == nil || err.Error() != testCase.wantMessage || settled != nil {
				t.Fatalf("invalid Execute = (%#v, %v)", settled, err)
			}
			entries := subject.conversation.Events()
			if len(entries) != 2 || entries[1].Type != DoneEventName {
				t.Fatalf("invalid lifecycle = %#v", entries)
			}
			var doneValue Done
			if err := json.Unmarshal(entries[1].Data, &doneValue); err != nil {
				t.Fatal(err)
			}
			if doneValue.Kind != ResultError || doneValue.Text == nil ||
				*doneValue.Text != testCase.wantMessage {
				t.Fatalf("invalid command/done = %#v", doneValue)
			}
		})
	}
}

func TestCommandRuntimeRejectsUnavailableImageCapabilityAndInvalidSourceLink(t *testing.T) {
	owner := newCommandRuntimeFixture(t)
	subject := newCommandAgentFixture(t)
	if _, err := owner.Register(Definition{
		Name:        "image",
		Description: "Needs images",
		Input: &InputDescriptor{
			Hint:   "image",
			Images: true,
		},
		Handler: func(context.Context, Invocation) (Result, error) {
			return successFixture("unexpected"), nil
		},
	}); err == nil || !strings.Contains(err.Error(), "image attachment admission capability") {
		t.Fatalf("image-capable registration error = %v", err)
	}
	if _, err := owner.Register(Definition{
		Name:        "badlink",
		Description: "Cite command lifecycle",
		Handler: func(context.Context, Invocation) (Result, error) {
			commandRunSequence := int64(0)
			return Result{
				Kind:           ResultSuccess,
				SourceEventSeq: &commandRunSequence,
			}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	settled, err := owner.Execute(
		context.Background(),
		subject,
		"/badlink",
		ExecuteOptions{},
	)
	if err == nil || !strings.Contains(err.Error(), "identifies a command lifecycle event") ||
		settled != nil {
		t.Fatalf("invalid source link = (%#v, %v)", settled, err)
	}
	if entries := subject.conversation.Events(); len(entries) != 1 ||
		entries[0].Type != RunEventName {
		t.Fatalf("invalid source link lifecycle = %#v", entries)
	}
}

func TestCommandRuntimeContainsObserverPanic(t *testing.T) {
	owner, err := NewCommandRuntime(RuntimeOptions{
		InstanceToken: "observer-panic",
		ObserverError: func(error) {
			panic("observer exploded")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	owner.reportFailure(errors.New("contained diagnostic"))
}

func newCommandRuntimeFixture(t *testing.T) *CommandRuntime {
	t.Helper()
	owner, err := NewCommandRuntime(RuntimeOptions{
		InstanceToken: "fixture",
		ObserverError: func(problem error) {
			t.Errorf("contained Commands failure: %v", problem)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := owner.Close(context.Background()); closeErr != nil {
			t.Errorf("close Commands: %v", closeErr)
		}
	})
	return owner
}

func newCommandAgentFixture(t *testing.T) *commandAgentFixture {
	t.Helper()
	conversation, err := session.New(
		session.SessionID("commands-fixture"),
		session.CreateOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	return &commandAgentFixture{
		conversation: conversation,
	}
}

func successFixture(message string) Result {
	return Result{
		Kind: ResultSuccess,
		Text: &message,
	}
}
