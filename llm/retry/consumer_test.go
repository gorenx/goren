package llmretry

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
)

type retrySubjectFixture struct {
	plugin.Base
	conversation session.Context
}

func (subject *retrySubjectFixture) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "llmretry-test-agent",
		Provides: []plugin.ProvidedService{
			plugin.NewProvidedService[agent.Agent](subject),
		},
	}
}

func (*retrySubjectFixture) Apply(requestContext context.Context) error {
	return requestContext.Err()
}

func (*retrySubjectFixture) Dispose(context.Context) error {
	return nil
}

func (subject *retrySubjectFixture) ID() session.SessionID {
	return subject.conversation.ID()
}

func (*retrySubjectFixture) OptionsValue() agent.Options {
	return agent.Options{}
}

func (subject *retrySubjectFixture) SessionValue() session.Context {
	return subject.conversation
}

func (*retrySubjectFixture) InboxValue() *agent.Inbox {
	return nil
}

func (*retrySubjectFixture) StatusValue() agent.Status {
	return agent.StatusIdle
}

func (*retrySubjectFixture) Cancel(agent.CancelCause, agent.CancelOptions) {}

func (*retrySubjectFixture) WhenIdle(context.Context) error {
	return nil
}

func (*retrySubjectFixture) RunMaintenance(
	requestContext context.Context,
	operation func(context.Context) error,
) error {
	return operation(requestContext)
}

func (*retrySubjectFixture) Send(llm.UserMessage, agent.InboxTarget, bool) error {
	return nil
}

func (*retrySubjectFixture) Followup(llm.UserMessage) error {
	return nil
}

func (*retrySubjectFixture) Steer(llm.UserMessage) error {
	return nil
}

func (*retrySubjectFixture) Inject(llm.UserMessage) error {
	return nil
}

type retryFixture struct {
	engine      *plugin.Runtime
	retryHandle plugin.Handle
	subject     *retrySubjectFixture
}

func newRetryFixture(
	t *testing.T,
	conversation session.Context,
	options RuntimeOptions,
) *retryFixture {
	t.Helper()
	registry := agent.NewRegistry(agent.RegistryOptions{})
	retryPlugin := New(options)
	subject := &retrySubjectFixture{
		conversation: conversation,
	}
	engine := plugin.NewRuntime(plugin.RuntimeSettings{})
	handles, err := engine.Start(
		context.Background(),
		registry,
		retryPlugin,
		subject,
	)
	if err != nil {
		t.Fatal(err)
	}
	state := &retryFixture{
		engine:      engine,
		retryHandle: handles[1],
		subject:     subject,
	}
	t.Cleanup(func() {
		if shutdownErr := engine.Shutdown(context.Background()); shutdownErr != nil {
			t.Error(shutdownErr)
		}
	})
	return state
}

func (state *retryFixture) resolve(
	requestContext context.Context,
	notice agent.RequestErrorNotice,
	terminal agent.RequestErrorActionFunc,
) (agent.RequestErrorAction, error) {
	notice.Subject = state.subject
	return agent.ResolveRequestError(requestContext, notice, terminal)
}

func TestNormalRetryRecordsBudgetAndWaitTransitions(t *testing.T) {
	conversation := openRetrySession(t, "mock")
	state := newRetryFixture(
		t,
		conversation,
		RuntimeOptions{
			Random: func() float64 {
				return 0
			},
			NewRetryID: func() (RetryID, error) {
				return "chain-1", nil
			},
		},
	)
	policy := llm.NormalRetryPolicy{
		ResolvedRetryBackoff: llm.ResolvedRetryBackoff{
			InitialDelayMS: 1,
			MaxDelayMS:     4,
			JitterRatio:    1,
		},
		Mode:           llm.RetryNormal,
		MaxRetries:     2,
		RetryableCodes: []string{"SERVER", "RATE_LIMIT"},
	}
	statusCode := 429
	notice := agent.RequestErrorNotice{
		Turn:     1,
		Step:     1,
		Provider: "mock",
		Failure: llm.LlmFailure{
			Message: "busy",
			Code:    "RATE_LIMIT",
			Status:  &statusCode,
		},
		RetryPolicy: policy,
	}
	downstreamCalls := 0
	terminal := agent.RequestErrorActionFunc(
		func(context.Context, agent.RequestErrorNotice) (agent.RequestErrorAction, error) {
			downstreamCalls++
			return agent.RequestErrorAction{}, nil
		},
	)
	for attempt := 0; attempt < 2; attempt++ {
		action, err := state.resolve(context.Background(), notice, terminal)
		if err != nil {
			t.Fatal(err)
		}
		if !action.Retry {
			t.Fatalf("attempt %d did not retry", attempt+1)
		}
	}
	action, err := state.resolve(context.Background(), notice, terminal)
	if err != nil {
		t.Fatal(err)
	}
	if action.Retry || downstreamCalls != 1 {
		t.Fatalf(
			"exhausted action = %#v, downstream calls = %d",
			action,
			downstreamCalls,
		)
	}

	events := conversation.Events()
	retryRecords := make([]retryFacts, 0, 2)
	retryTypes := make([]string, 0, 4)
	for _, committed := range events {
		switch committed.Type {
		case RetryEventName:
			record, decodeErr := DecodeRetryRecord(committed.Data)
			if decodeErr != nil {
				t.Fatal(decodeErr)
			}
			facts, factsErr := factsFromRecord(record)
			if factsErr != nil {
				t.Fatal(factsErr)
			}
			retryRecords = append(retryRecords, facts)
			retryTypes = append(retryTypes, committed.Type)
		case RetryStartedEventName:
			retryTypes = append(retryTypes, committed.Type)
		}
	}
	if strings.Join(retryTypes, ",") !=
		"llm/retry,llm/retry-started,llm/retry,llm/retry-started" {
		t.Fatalf("retry event order = %#v", retryTypes)
	}
	if len(retryRecords) != 2 ||
		retryRecords[0].retry != 1 ||
		retryRecords[1].retry != 2 ||
		retryRecords[0].chainID != "chain-1" ||
		retryRecords[1].chainID != "chain-1" ||
		retryRecords[0].delayMS != 0 ||
		retryRecords[1].delayMS != 0 ||
		retryRecords[0].policyKey !=
			`["normal",2,["RATE_LIMIT","SERVER"],1,4,1]` {
		t.Fatalf("retry records = %#v", retryRecords)
	}
	if err := ValidateHistory(events); err != nil {
		t.Fatal(err)
	}
	if len(conversation.Surface().Nodes) != 0 {
		t.Fatalf("retry records entered model surface: %#v", conversation.Surface())
	}
}

func TestNormalRetryDelegatesNonTransientAndOverCapRetryAfter(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		label   string
		failure llm.LlmFailure
	}{
		{
			label: "non-transient",
			failure: llm.LlmFailure{
				Message: "bad key",
				Code:    "AUTH",
			},
		},
		{
			label: "over-cap",
			failure: llm.LlmFailure{
				Message:              "wait too long",
				Code:                 "SERVER",
				ProviderRetryAfterMS: floatPointer(5),
			},
		},
	} {
		testCase := testCase
		t.Run(testCase.label, func(t *testing.T) {
			t.Parallel()
			conversation := openRetrySession(t, "mock")
			state := newRetryFixture(
				t,
				conversation,
				RuntimeOptions{
					Random: func() float64 {
						return 0.5
					},
				},
			)
			policy := llm.NormalRetryPolicy{
				ResolvedRetryBackoff: llm.ResolvedRetryBackoff{
					InitialDelayMS: 1,
					MaxDelayMS:     4,
					JitterRatio:    0,
				},
				Mode:           llm.RetryNormal,
				MaxRetries:     2,
				RetryableCodes: []string{"SERVER"},
			}
			delegated := 0
			action, err := state.resolve(
				context.Background(),
				agent.RequestErrorNotice{
					Turn:        1,
					Step:        1,
					Provider:    "mock",
					Failure:     testCase.failure,
					RetryPolicy: policy,
				},
				agent.RequestErrorActionFunc(
					func(context.Context, agent.RequestErrorNotice) (agent.RequestErrorAction, error) {
						delegated++
						return agent.RequestErrorAction{}, nil
					},
				),
			)
			if err != nil {
				t.Fatal(err)
			}
			if action.Retry || delegated != 1 || countRetryEvents(conversation) != 0 {
				t.Fatalf(
					"action = %#v, delegated = %d, retries = %d",
					action,
					delegated,
					countRetryEvents(conversation),
				)
			}
		})
	}
}

func TestAlwaysRetryGivesDownstreamRecoveryPrecedenceAndContainsFailure(t *testing.T) {
	t.Run("decision wins", func(t *testing.T) {
		conversation := openRetrySession(t, "mock")
		state := newRetryFixture(
			t,
			conversation,
			RuntimeOptions{
				Random: func() float64 {
					return 0
				},
			},
		)
		action, err := state.resolve(
			context.Background(),
			alwaysNotice(),
			agent.RequestErrorActionFunc(
				func(context.Context, agent.RequestErrorNotice) (agent.RequestErrorAction, error) {
					return agent.RequestErrorAction{
						Retry: true,
					}, nil
				},
			),
		)
		if err != nil {
			t.Fatal(err)
		}
		if !action.Retry || countRetryEvents(conversation) != 0 {
			t.Fatalf(
				"action = %#v, retries = %d",
				action,
				countRetryEvents(conversation),
			)
		}
	})

	t.Run("failure falls back", func(t *testing.T) {
		conversation := openRetrySession(t, "mock")
		reported := make(chan error, 1)
		state := newRetryFixture(
			t,
			conversation,
			RuntimeOptions{
				Random: func() float64 {
					return 0
				},
				NewRetryID: func() (RetryID, error) {
					return "always-chain", nil
				},
				ObserverError: func(problem error) {
					reported <- problem
				},
			},
		)
		notice := alwaysNotice()
		notice.Failure.ProviderRetryAfterMS = floatPointer(2)
		action, err := state.resolve(
			context.Background(),
			notice,
			agent.RequestErrorActionFunc(
				func(context.Context, agent.RequestErrorNotice) (agent.RequestErrorAction, error) {
					return agent.RequestErrorAction{}, errors.New(
						"specialized recovery failed",
					)
				},
			),
		)
		if err != nil {
			t.Fatal(err)
		}
		if !action.Retry || countRetryEvents(conversation) != 1 {
			t.Fatalf(
				"action = %#v, retries = %d",
				action,
				countRetryEvents(conversation),
			)
		}
		select {
		case problem := <-reported:
			if !strings.Contains(problem.Error(), "specialized recovery failed") {
				t.Fatalf("reported error = %v", problem)
			}
		default:
			t.Fatal("downstream failure was not reported")
		}
	})
}

func TestRuntimeUnloadCancelsBackoffAndWithdrawsMiddleware(t *testing.T) {
	conversation := openRetrySession(t, "mock")
	state := newRetryFixture(
		t,
		conversation,
		RuntimeOptions{
			Random: func() float64 {
				return 0.5
			},
			NewRetryID: func() (RetryID, error) {
				return "cancel-chain", nil
			},
		},
	)
	policy := llm.AlwaysRetryPolicy{
		ResolvedRetryBackoff: llm.ResolvedRetryBackoff{
			InitialDelayMS: 60_000,
			MaxDelayMS:     60_000,
			JitterRatio:    0,
		},
		Mode: llm.RetryAlways,
	}
	notice := agent.RequestErrorNotice{
		Turn:     1,
		Step:     1,
		Provider: "mock",
		Failure: llm.LlmFailure{
			Message: "offline",
			Code:    "TRANSPORT",
		},
		RetryPolicy: policy,
	}
	type result struct {
		action agent.RequestErrorAction
		err    error
	}
	settled := make(chan result, 1)
	go func() {
		action, err := state.resolve(
			context.Background(),
			notice,
			agent.RequestErrorActionFunc(
				func(context.Context, agent.RequestErrorNotice) (agent.RequestErrorAction, error) {
					return agent.RequestErrorAction{}, nil
				},
			),
		)
		settled <- result{
			action: action,
			err:    err,
		}
	}()
	waitForScheduledRetry(t, conversation)
	if err := state.engine.Unload(context.Background(), state.retryHandle); err != nil {
		t.Fatal(err)
	}
	select {
	case outcome := <-settled:
		if outcome.err != nil || outcome.action.Retry {
			t.Fatalf("settled outcome = %#v", outcome)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled retry did not drain")
	}
	if countEventType(conversation, RetryStartedEventName) != 0 {
		t.Fatal("cancelled wait appended retry-started")
	}
	downstreamCalls := 0
	action, err := state.resolve(
		context.Background(),
		notice,
		agent.RequestErrorActionFunc(
			func(context.Context, agent.RequestErrorNotice) (agent.RequestErrorAction, error) {
				downstreamCalls++
				return agent.RequestErrorAction{
					Retry: true,
				}, nil
			},
		),
	)
	if err != nil || !action.Retry || downstreamCalls != 1 {
		t.Fatalf(
			"post-unload action = %#v, error = %v, downstream = %d",
			action,
			err,
			downstreamCalls,
		)
	}
}

func TestRuntimeUnloadWaitsForDelegatedRecovery(t *testing.T) {
	conversation := openRetrySession(t, "mock")
	state := newRetryFixture(
		t,
		conversation,
		RuntimeOptions{
			Random: func() float64 {
				return 0.5
			},
		},
	)
	entered := make(chan struct{})
	release := make(chan struct{})
	settled := make(chan struct{})
	go func() {
		_, _ = state.resolve(
			context.Background(),
			alwaysNotice(),
			agent.RequestErrorActionFunc(
				func(context.Context, agent.RequestErrorNotice) (agent.RequestErrorAction, error) {
					close(entered)
					<-release
					return agent.RequestErrorAction{
						Retry: true,
					}, nil
				},
			),
		)
		close(settled)
	}()
	<-entered
	unloaded := make(chan error, 1)
	go func() {
		unloaded <- state.engine.Unload(context.Background(), state.retryHandle)
	}()
	select {
	case err := <-unloaded:
		t.Fatalf("unload returned before downstream settled: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-unloaded:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("unload did not drain delegated recovery")
	}
	<-settled
	if countRetryEvents(conversation) != 0 {
		t.Fatal("cancelled delegated recovery entered fallback")
	}
}

func TestMintRetryIDProducesUUIDV4(t *testing.T) {
	t.Parallel()
	identifier, err := mintRetryID()
	if err != nil {
		t.Fatal(err)
	}
	pattern := regexp.MustCompile(
		`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`,
	)
	if !pattern.MatchString(string(identifier)) {
		t.Fatalf("retry id = %q", identifier)
	}
}

func alwaysNotice() agent.RequestErrorNotice {
	return agent.RequestErrorNotice{
		Turn:     1,
		Step:     1,
		Provider: "mock",
		Failure: llm.LlmFailure{
			Message: "auth",
			Code:    "AUTH",
		},
		RetryPolicy: llm.AlwaysRetryPolicy{
			ResolvedRetryBackoff: llm.ResolvedRetryBackoff{
				InitialDelayMS: 1,
				MaxDelayMS:     1,
				JitterRatio:    1,
			},
			Mode: llm.RetryAlways,
		},
	}
}

func waitForScheduledRetry(t *testing.T, conversation session.Context) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if countRetryEvents(conversation) != 0 {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("retry was not scheduled")
		case <-ticker.C:
		}
	}
}

func countRetryEvents(conversation session.Context) int {
	return countEventType(conversation, RetryEventName)
}

func countEventType(conversation session.Context, eventType string) int {
	count := 0
	for _, committed := range conversation.Events() {
		if committed.Type == eventType {
			count++
		}
	}
	return count
}

func floatPointer(value float64) *float64 {
	return &value
}
