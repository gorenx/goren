package command

import (
	"context"
	"errors"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/commands"
	"github.com/gorenx/goren/compaction"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
)

type commandCompactionStub struct {
	outcome *compaction.Result
	failure error
	calls   []commandCompactionCall
}

type commandCompactionCall struct {
	subject         compaction.ManualAgentContext
	sourceCommandID *string
}

func (*commandCompactionStub) CompactIfNeeded(
	context.Context,
	compaction.AgentContext,
	compaction.Trigger,
) (*compaction.Result, error) {
	return nil, nil
}

func (backend *commandCompactionStub) CompactNow(
	_ context.Context,
	subject compaction.ManualAgentContext,
	sourceCommandID *string,
) (*compaction.Result, error) {
	backend.calls = append(backend.calls, commandCompactionCall{
		subject:         subject,
		sourceCommandID: cloneCommandString(sourceCommandID),
	})
	return backend.outcome, backend.failure
}

func (*commandCompactionStub) CompactRegion(
	context.Context,
	int64,
	int64,
	compaction.AgentContext,
) (compaction.Result, error) {
	return compaction.Result{}, nil
}

type compactAgentFixture struct {
	plugin.Base
	conversation *session.Session
}

func (subject *compactAgentFixture) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "command-compact-test-agent",
		Provides: []plugin.ProvidedService{
			plugin.NewProvidedService[agent.Agent](subject),
		},
	}
}

func (*compactAgentFixture) Apply(requestContext context.Context) error {
	return requestContext.Err()
}

func (*compactAgentFixture) Dispose(context.Context) error { return nil }

func (subject *compactAgentFixture) ID() session.SessionID {
	return subject.conversation.ID()
}

func (*compactAgentFixture) OptionsValue() agent.Options { return agent.Options{} }

func (subject *compactAgentFixture) SessionValue() *session.Session {
	return subject.conversation
}

func (*compactAgentFixture) InboxValue() *agent.Inbox { return nil }

func (*compactAgentFixture) StatusValue() agent.Status { return agent.StatusIdle }

func (*compactAgentFixture) Cancel(agent.CancelCause, agent.CancelOptions) {}

func (*compactAgentFixture) WhenIdle(context.Context) error { return nil }

func (*compactAgentFixture) RunMaintenance(
	requestContext context.Context,
	operation func(context.Context) error,
) error {
	return operation(requestContext)
}

func (*compactAgentFixture) Send(llm.UserMessage, agent.InboxTarget, bool) error { return nil }

func (*compactAgentFixture) Followup(llm.UserMessage) error { return nil }

func (*compactAgentFixture) Steer(llm.UserMessage) error { return nil }

func (*compactAgentFixture) Inject(llm.UserMessage) error { return nil }

func TestCompactReportsSuccessAndForwardsCommandProvenance(t *testing.T) {
	subject := newCompactAgentFixture(t)
	backend := &commandCompactionStub{
		outcome: &compaction.Result{
			SummarySeq:         8,
			ShadowedSeqs:       []int64{1, 3, 7},
			ShadowedTokenCount: 42,
		},
	}
	operation := &Compact{
		compactor: backend,
	}
	outcome, err := operation.Execute(
		context.Background(),
		commands.Invocation{
			CommandID: "command-fixture",
			Agent:     subject,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != commands.ResultSuccess || outcome.Text == nil ||
		*outcome.Text != "Compacted 3 history items (~42 tokens)." ||
		outcome.SourceEventSeq == nil || *outcome.SourceEventSeq != 8 {
		t.Fatalf("success result = %#v", outcome)
	}
	if len(backend.calls) != 1 || backend.calls[0].subject != subject ||
		backend.calls[0].sourceCommandID == nil ||
		*backend.calls[0].sourceCommandID != "command-fixture" {
		t.Fatalf("CompactNow calls = %#v", backend.calls)
	}
}

func TestCompactRejectsArgumentsAndReportsNoHistory(t *testing.T) {
	subject := newCompactAgentFixture(t)
	backend := &commandCompactionStub{}
	operation := &Compact{
		compactor: backend,
	}
	rejected, err := operation.Execute(
		context.Background(),
		commands.Invocation{
			CommandID: "command-args",
			Agent:     subject,
			RawInput:  " now",
		},
	)
	if err != nil || rejected.Kind != commands.ResultError ||
		rejected.Text == nil || *rejected.Text != usage || len(backend.calls) != 0 {
		t.Fatalf("argument rejection = (%#v, %v), calls %#v", rejected, err, backend.calls)
	}
	empty, err := operation.Execute(
		context.Background(),
		commands.Invocation{
			CommandID: "command-empty",
			Agent:     subject,
		},
	)
	if err != nil || empty.Kind != commands.ResultSuccess || empty.Text == nil ||
		*empty.Text != "No compactable history yet." || empty.SourceEventSeq != nil {
		t.Fatalf("empty result = (%#v, %v)", empty, err)
	}
}

func TestCompactMapsEveryExpectedManualFailure(t *testing.T) {
	testCases := []struct {
		code compaction.ManualErrorCode
		text string
	}{
		{
			code: compaction.ManualErrorBusy,
			text: "Compaction is unavailable because this process has an active compaction, or the agent is not idle.",
		},
		{
			code: compaction.ManualErrorCancelled,
			text: "Compaction cancelled.",
		},
		{
			code: compaction.ManualErrorChanged,
			text: "The history selected for compaction changed before it could be replaced. The conversation is unchanged; the attempt is recorded in the session log.",
		},
		{
			code: compaction.ManualErrorSummary,
			text: "Compaction could not produce a useful summary. The conversation is unchanged; the attempt is recorded in the session log.",
		},
		{
			code: compaction.ManualErrorCommit,
			text: "Compaction did not finish cleanly; some session history may have changed. Inspect the current session state before retrying.",
		},
		{
			code: compaction.ManualErrorPersistence,
			text: "Compaction finished, but the session could not be saved.",
		},
	}
	for _, testCase := range testCases {
		t.Run(string(testCase.code), func(t *testing.T) {
			backend := &commandCompactionStub{
				failure: &compaction.ManualError{
					Code:    testCase.code,
					Message: "backend detail",
				},
			}
			operation := &Compact{
				compactor: backend,
			}
			outcome, err := operation.Execute(
				context.Background(),
				commands.Invocation{
					CommandID: "command-error",
					Agent:     newCompactAgentFixture(t),
				},
			)
			if err != nil || outcome.Kind != commands.ResultError ||
				outcome.Text == nil || *outcome.Text != testCase.text {
				t.Fatalf("mapped failure = (%#v, %v)", outcome, err)
			}
		})
	}
}

func TestCompactPreservesUnexpectedFailureAndCancellationAuthority(t *testing.T) {
	unexpected := errors.New("unexpected backend failure")
	backend := &commandCompactionStub{
		failure: unexpected,
	}
	operation := &Compact{
		compactor: backend,
	}
	_, err := operation.Execute(
		context.Background(),
		commands.Invocation{
			CommandID: "command-bug",
			Agent:     newCompactAgentFixture(t),
		},
	)
	if !errors.Is(err, unexpected) {
		t.Fatalf("unexpected error = %v", err)
	}

	requestContext, cancelRequest := context.WithCancel(context.Background())
	cancelRequest()
	backend.failure = &compaction.ManualError{
		Code:    compaction.ManualErrorSummary,
		Message: "late summary classification",
	}
	outcome, err := operation.Execute(
		requestContext,
		commands.Invocation{
			CommandID: "command-cancelled",
			Agent:     newCompactAgentFixture(t),
		},
	)
	if err != nil || outcome.Kind != commands.ResultError || outcome.Text == nil ||
		*outcome.Text != "Compaction cancelled." {
		t.Fatalf("cancelled result = (%#v, %v)", outcome, err)
	}
}

func newCompactAgentFixture(t *testing.T) *compactAgentFixture {
	t.Helper()
	conversation, err := session.New(
		"command-compact-fixture",
		session.CreateOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	return &compactAgentFixture{
		conversation: conversation,
	}
}

func cloneCommandString(source *string) *string {
	if source == nil {
		return nil
	}
	detached := *source
	return &detached
}

var _ compaction.Engine = (*commandCompactionStub)(nil)
