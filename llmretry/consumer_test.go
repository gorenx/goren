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
	agent.Agent
	conversation *session.Session
}

func (subject *retrySubjectFixture) SessionValue() *session.Session { return subject.conversation }
func (*retrySubjectFixture) ScopeValue() *plugin.Scope              { return nil }

func TestNormalRetryRecordsBudgetAndWaitTransitions(t *testing.T) {
	conversation := openRetrySession(t, "mock")
	subject := &retrySubjectFixture{conversation: conversation}
	owner := newRetryConsumerFixture(func() float64 { return 0 }, nil)
	owner.mintID = func() (RetryID, error) { return "chain-1", nil }
	policy := llm.NormalRetryPolicy{
		ResolvedRetryBackoff: llm.ResolvedRetryBackoff{InitialDelayMS: 1, MaxDelayMS: 4, JitterRatio: 1},
		Mode:                 llm.RetryNormal, MaxRetries: 2, RetryableCodes: []string{"SERVER", "RATE_LIMIT"},
	}
	statusCode := 429
	notice := agent.RequestErrorNotice{
		Subject: subject, Turn: 1, Step: 1, Provider: "mock", RetryPolicy: policy,
		Failure: llm.LlmFailure{Message: "busy", Code: "RATE_LIMIT", Status: &statusCode},
	}
	downstreamCalls := 0
	downstream := func(context.Context) (agent.RequestErrorAction, error) {
		downstreamCalls++
		return agent.RequestErrorAction{}, nil
	}
	for attempt := 0; attempt < 2; attempt++ {
		action, err := owner.resolve(context.Background(), notice, downstream)
		if err != nil {
			t.Fatal(err)
		}
		if !action.Retry {
			t.Fatalf("attempt %d did not retry", attempt+1)
		}
	}
	action, err := owner.resolve(context.Background(), notice, downstream)
	if err != nil {
		t.Fatal(err)
	}
	if action.Retry || downstreamCalls != 1 {
		t.Fatalf("exhausted action = %#v, downstream calls = %d", action, downstreamCalls)
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
	if strings.Join(retryTypes, ",") != "llm/retry,llm/retry-started,llm/retry,llm/retry-started" {
		t.Fatalf("retry event order = %#v", retryTypes)
	}
	if len(retryRecords) != 2 || retryRecords[0].retry != 1 || retryRecords[1].retry != 2 ||
		retryRecords[0].chainID != "chain-1" || retryRecords[1].chainID != "chain-1" ||
		retryRecords[0].delayMS != 0 || retryRecords[1].delayMS != 0 ||
		retryRecords[0].policyKey != `["normal",2,["RATE_LIMIT","SERVER"],1,4,1]` {
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
		{label: "non-transient", failure: llm.LlmFailure{Message: "bad key", Code: "AUTH"}},
		{label: "over-cap", failure: llm.LlmFailure{
			Message: "wait too long", Code: "SERVER", ProviderRetryAfterMS: floatPointer(5),
		}},
	} {
		testCase := testCase
		t.Run(testCase.label, func(t *testing.T) {
			t.Parallel()
			conversation := openRetrySession(t, "mock")
			subject := &retrySubjectFixture{conversation: conversation}
			owner := newRetryConsumerFixture(func() float64 { return 0.5 }, nil)
			policy := llm.NormalRetryPolicy{
				ResolvedRetryBackoff: llm.ResolvedRetryBackoff{InitialDelayMS: 1, MaxDelayMS: 4, JitterRatio: 0},
				Mode:                 llm.RetryNormal, MaxRetries: 2, RetryableCodes: []string{"SERVER"},
			}
			delegated := 0
			action, err := owner.resolve(context.Background(), agent.RequestErrorNotice{
				Subject: subject, Turn: 1, Step: 1, Provider: "mock",
				Failure: testCase.failure, RetryPolicy: policy,
			}, func(context.Context) (agent.RequestErrorAction, error) {
				delegated++
				return agent.RequestErrorAction{}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if action.Retry || delegated != 1 || countRetryEvents(conversation) != 0 {
				t.Fatalf("action = %#v, delegated = %d, retries = %d", action, delegated, countRetryEvents(conversation))
			}
		})
	}
}

func TestAlwaysRetryGivesDownstreamRecoveryPrecedenceAndContainsFailure(t *testing.T) {
	t.Run("decision wins", func(t *testing.T) {
		conversation := openRetrySession(t, "mock")
		owner := newRetryConsumerFixture(func() float64 { return 0 }, nil)
		action, err := owner.resolve(context.Background(), alwaysNotice(conversation),
			func(context.Context) (agent.RequestErrorAction, error) {
				return agent.RequestErrorAction{Retry: true}, nil
			})
		if err != nil {
			t.Fatal(err)
		}
		if !action.Retry || countRetryEvents(conversation) != 0 {
			t.Fatalf("action = %#v, retries = %d", action, countRetryEvents(conversation))
		}
	})

	t.Run("failure falls back", func(t *testing.T) {
		conversation := openRetrySession(t, "mock")
		reported := make(chan error, 1)
		owner := newRetryConsumerFixture(func() float64 { return 0 }, func(problem error) { reported <- problem })
		owner.mintID = func() (RetryID, error) { return "always-chain", nil }
		notice := alwaysNotice(conversation)
		notice.Failure.ProviderRetryAfterMS = floatPointer(2)
		action, err := owner.resolve(context.Background(), notice,
			func(context.Context) (agent.RequestErrorAction, error) {
				return agent.RequestErrorAction{}, errors.New("specialized recovery failed")
			})
		if err != nil {
			t.Fatal(err)
		}
		if !action.Retry || countRetryEvents(conversation) != 1 {
			t.Fatalf("action = %#v, retries = %d", action, countRetryEvents(conversation))
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

func TestConsumerCloseCancelsBackoffDrainsAndRejectsCapturedHandler(t *testing.T) {
	conversation := openRetrySession(t, "mock")
	owner := newRetryConsumerFixture(func() float64 { return 0.5 }, nil)
	owner.mintID = func() (RetryID, error) { return "cancel-chain", nil }
	policy := llm.AlwaysRetryPolicy{
		ResolvedRetryBackoff: llm.ResolvedRetryBackoff{InitialDelayMS: 60_000, MaxDelayMS: 60_000, JitterRatio: 0},
		Mode:                 llm.RetryAlways,
	}
	notice := agent.RequestErrorNotice{
		Subject: &retrySubjectFixture{conversation: conversation}, Turn: 1, Step: 1, Provider: "mock",
		Failure: llm.LlmFailure{Message: "offline", Code: "TRANSPORT"}, RetryPolicy: policy,
	}
	type result struct {
		action agent.RequestErrorAction
		err    error
	}
	settled := make(chan result, 1)
	go func() {
		action, err := owner.handle(context.Background(), notice,
			func(context.Context) (agent.RequestErrorAction, error) { return agent.RequestErrorAction{}, nil })
		settled <- result{action: action, err: err}
	}()
	waitForScheduledRetry(t, conversation)
	if err := owner.close(context.Background()); err != nil {
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
	action, err := owner.handle(context.Background(), notice,
		func(context.Context) (agent.RequestErrorAction, error) {
			downstreamCalls++
			return agent.RequestErrorAction{Retry: true}, nil
		})
	if err != nil || action.Retry || downstreamCalls != 0 {
		t.Fatalf("captured handler action = %#v, error = %v, downstream = %d", action, err, downstreamCalls)
	}
}

func TestConsumerCloseWaitsForDelegatedRecovery(t *testing.T) {
	conversation := openRetrySession(t, "mock")
	owner := newRetryConsumerFixture(func() float64 { return 0.5 }, nil)
	entered := make(chan struct{})
	release := make(chan struct{})
	settled := make(chan struct{})
	go func() {
		_, _ = owner.handle(context.Background(), alwaysNotice(conversation),
			func(context.Context) (agent.RequestErrorAction, error) {
				close(entered)
				<-release
				return agent.RequestErrorAction{Retry: true}, nil
			})
		close(settled)
	}()
	<-entered
	closed := make(chan error, 1)
	go func() { closed <- owner.close(context.Background()) }()
	select {
	case err := <-closed:
		t.Fatalf("close returned before downstream settled: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("close did not drain delegated recovery")
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
	pattern := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	if !pattern.MatchString(string(identifier)) {
		t.Fatalf("retry id = %q", identifier)
	}
}

func newRetryConsumerFixture(randomSample func() float64, reporter func(error)) *retryConsumer {
	lifetime, cancelLifetime := context.WithCancel(context.Background())
	if reporter == nil {
		reporter = func(error) {}
	}
	return &retryConsumer{
		lifetime: lifetime, cancelLifetime: cancelLifetime,
		randomSample: randomSample, mintID: func() (RetryID, error) { return "chain", nil }, report: reporter,
	}
}

func alwaysNotice(conversation *session.Session) agent.RequestErrorNotice {
	return agent.RequestErrorNotice{
		Subject: &retrySubjectFixture{conversation: conversation}, Turn: 1, Step: 1, Provider: "mock",
		Failure: llm.LlmFailure{Message: "auth", Code: "AUTH"},
		RetryPolicy: llm.AlwaysRetryPolicy{
			ResolvedRetryBackoff: llm.ResolvedRetryBackoff{InitialDelayMS: 1, MaxDelayMS: 1, JitterRatio: 1},
			Mode:                 llm.RetryAlways,
		},
	}
}

func waitForScheduledRetry(t *testing.T, conversation *session.Session) {
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

func countRetryEvents(conversation *session.Session) int {
	return countEventType(conversation, RetryEventName)
}

func countEventType(conversation *session.Session, eventType string) int {
	count := 0
	for _, committed := range conversation.Events() {
		if committed.Type == eventType {
			count++
		}
	}
	return count
}

func floatPointer(value float64) *float64 { return &value }
