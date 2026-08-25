package userquestions_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	agentcore "github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/userquestions"
)

type questionsFixture struct {
	engine         *plugin.Runtime
	registry       *questionRegistry
	registryHandle plugin.Handle
	questions      userquestions.UserQuestions
}

type questionsFixturePlugin struct {
	plugin.Base
	fixture *questionsFixture
}

func (*questionsFixturePlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "userquestions-test-consumer",
		Requires: []plugin.ServiceType{
			plugin.ServiceOf[userquestions.UserQuestions](),
		},
	}
}

func (owner *questionsFixturePlugin) Apply(context.Context) error {
	questions, err := plugin.Require[userquestions.UserQuestions](owner)
	if err != nil {
		return err
	}
	owner.fixture.questions = questions
	return nil
}

func (*questionsFixturePlugin) Dispose(context.Context) error { return nil }

var _ plugin.Plugin = (*questionsFixturePlugin)(nil)

type questionRegistry struct {
	mutex   sync.RWMutex
	entries map[session.SessionID]agentcore.Agent
}

func newQuestionRegistry() *questionRegistry {
	return &questionRegistry{
		entries: make(map[session.SessionID]agentcore.Agent),
	}
}

func (registry *questionRegistry) Get(
	identifier session.SessionID,
) (agentcore.Agent, bool) {
	registry.mutex.RLock()
	subject, found := registry.entries[identifier]
	registry.mutex.RUnlock()
	return subject, found
}

func (registry *questionRegistry) Contains(subject agentcore.Agent) bool {
	if subject == nil {
		return false
	}
	current, found := registry.Get(subject.ID())
	return found && agentcore.Same(current, subject)
}

func (registry *questionRegistry) List() []agentcore.Agent {
	registry.mutex.RLock()
	result := make([]agentcore.Agent, 0, len(registry.entries))
	for _, subject := range registry.entries {
		result = append(result, subject)
	}
	registry.mutex.RUnlock()
	return result
}

func (registry *questionRegistry) enter(subject agentcore.Agent) error {
	registry.mutex.Lock()
	defer registry.mutex.Unlock()
	if registry.entries[subject.ID()] != nil {
		return errors.New("test: duplicate Agent")
	}
	registry.entries[subject.ID()] = subject
	return nil
}

type questionRegistryPlugin struct {
	plugin.Base
	registry *questionRegistry
}

func (adapter *questionRegistryPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "userquestions-test-registry",
		Provides: []plugin.ProvidedService{
			plugin.NewProvidedService[agentcore.Registry](adapter.registry),
		},
	}
}

func (*questionRegistryPlugin) Apply(requestContext context.Context) error {
	return requestContext.Err()
}

func (*questionRegistryPlugin) Dispose(context.Context) error { return nil }

var _ agentcore.Registry = (*questionRegistry)(nil)
var _ plugin.Plugin = (*questionRegistryPlugin)(nil)

type questionSubject struct {
	plugin.Base
	identifier   session.SessionID
	conversation session.Context
}

func (subject *questionSubject) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "userquestions-test-agent:" + string(subject.identifier),
		Provides: []plugin.ProvidedService{
			plugin.NewProvidedService[agentcore.Agent](subject),
		},
	}
}

func (*questionSubject) Apply(requestContext context.Context) error {
	return requestContext.Err()
}

func (*questionSubject) Dispose(context.Context) error { return nil }

func (subject *questionSubject) ID() session.SessionID {
	return subject.identifier
}

func (*questionSubject) OptionsValue() agentcore.Options {
	return agentcore.Options{}
}

func (subject *questionSubject) SessionValue() session.Context {
	return subject.conversation
}

func (*questionSubject) InboxValue() *agentcore.Inbox {
	return nil
}

func (*questionSubject) StatusValue() agentcore.Status {
	return agentcore.StatusIdle
}

func (*questionSubject) Cancel(agentcore.CancelCause, agentcore.CancelOptions) {}

func (*questionSubject) WhenIdle(context.Context) error {
	return nil
}

func (*questionSubject) Send(llm.UserMessage, agentcore.InboxTarget, bool) error {
	return nil
}

func (*questionSubject) Followup(llm.UserMessage) error {
	return nil
}

func (*questionSubject) Steer(llm.UserMessage) error {
	return nil
}

func (*questionSubject) Inject(llm.UserMessage) error {
	return nil
}

func (*questionSubject) RunMaintenance(
	requestContext context.Context,
	operation func(context.Context) error,
) error {
	return operation(requestContext)
}

type recordingProvider struct {
	requests []userquestions.Request
	answer   userquestions.Answer
	err      error
}

func (recorder *recordingProvider) Ask(
	_ context.Context,
	questionRequest userquestions.Request,
) (userquestions.Answer, error) {
	recorder.requests = append(recorder.requests, questionRequest)
	return recorder.answer, recorder.err
}

func newQuestionsFixture(t *testing.T, withRegistry bool) *questionsFixture {
	t.Helper()
	state := &questionsFixture{
		engine: plugin.NewRuntime(plugin.RuntimeSettings{}),
	}
	questionsPlugin := userquestions.NewPlugin()
	consumerPlugin := &questionsFixturePlugin{
		fixture: state,
	}
	instances := []plugin.Plugin{
		questionsPlugin,
		consumerPlugin,
	}
	if withRegistry {
		state.registry = newQuestionRegistry()
		instances = []plugin.Plugin{
			&questionRegistryPlugin{
				registry: state.registry,
			},
			questionsPlugin,
			consumerPlugin,
		}
	}
	handles, err := state.engine.Start(context.Background(), instances...)
	if err != nil {
		t.Fatal(err)
	}
	if withRegistry {
		state.registryHandle = handles[0]
	}
	t.Cleanup(func() {
		if shutdownErr := state.engine.Shutdown(context.Background()); shutdownErr != nil {
			t.Error(shutdownErr)
		}
	})
	return state
}

func newQuestionSubject(
	t *testing.T,
	state *questionsFixture,
	identifier session.SessionID,
	origin session.Origin,
) *questionSubject {
	t.Helper()
	conversation, err := session.New(
		identifier,
		session.CreateOptions{
			Metadata: session.Metadata{
				Origin: origin,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	subject := &questionSubject{
		identifier:   identifier,
		conversation: conversation,
	}
	if _, err = state.engine.MountScopedChild(
		context.Background(),
		state.registryHandle,
		subject,
	); err != nil {
		t.Fatal(err)
	}
	return subject
}

func requireQuestionCode(t *testing.T, err error, wantCode string) {
	t.Helper()
	var problem *userquestions.Error
	if !errors.As(err, &problem) || problem.Code != wantCode {
		t.Fatalf("error = %#v, want UserQuestionError code %q", err, wantCode)
	}
}

func TestProviderLifecycleAndDetachedDelegation(t *testing.T) {
	t.Parallel()
	state := newQuestionsFixture(t, false)
	answerText := "because it is reproducible"
	providerValue := &recordingProvider{
		answer: userquestions.Answer{
			Answers: []userquestions.AnswerItem{
				{
					ID:       "pkg",
					Selected: []string{"pnpm"},
					Custom:   &answerText,
				},
			},
		},
	}
	binding, err := state.questions.RegisterProvider(providerValue)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = state.questions.RegisterProvider(&recordingProvider{}); err == nil {
		t.Fatal("duplicate provider registration succeeded")
	} else {
		requireQuestionCode(t, err, userquestions.CodeDuplicate)
	}
	questions := []userquestions.Question{
		{
			ID:       "pkg",
			Question: "Which package manager?",
		},
	}
	answerValue, err := state.questions.Ask(
		context.Background(),
		userquestions.Request{
			Questions: questions,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(answerValue, providerValue.answer) || len(providerValue.requests) != 1 {
		t.Fatalf("delegation = (%#v, %#v)", answerValue, providerValue.requests)
	}
	questions[0].Question = "mutated"
	if providerValue.requests[0].Questions[0].Question != "Which package manager?" {
		t.Fatal("provider request borrowed caller slice")
	}
	answerValue.Answers[0].Selected[0] = "npm"
	if providerValue.answer.Answers[0].Selected[0] != "pnpm" {
		t.Fatal("returned answer borrowed provider slice")
	}
	binding.Unregister()
	binding.Unregister()
	_, err = state.questions.Ask(
		context.Background(),
		userquestions.Request{
			Questions: questions,
		},
	)
	requireQuestionCode(t, err, userquestions.CodeNoProvider)
}

func TestAskRejectsInvalidInputsBeforeProvider(t *testing.T) {
	t.Parallel()
	state := newQuestionsFixture(t, false)
	providerValue := &recordingProvider{}
	if _, err := state.questions.RegisterProvider(providerValue); err != nil {
		t.Fatal(err)
	}
	abortedContext, cancelRequest := context.WithCancel(context.Background())
	cancelRequest()
	_, err := state.questions.Ask(
		abortedContext,
		userquestions.Request{
			Questions: []userquestions.Question{
				{
					ID:       "confirm",
					Question: "Proceed?",
				},
			},
		},
	)
	requireQuestionCode(t, err, userquestions.CodeAborted)
	_, err = state.questions.Ask(context.Background(), userquestions.Request{})
	requireQuestionCode(t, err, userquestions.CodeEmptyQuestions)
	options := []userquestions.Option{
		{
			Label: "Approve",
		},
	}
	detail := "# Plan"
	_, err = state.questions.Ask(
		context.Background(),
		userquestions.Request{
			Questions: []userquestions.Question{
				{
					ID:       "plan",
					Question: "Approve?",
					Detail:   &detail,
					Options:  &options,
					Intent: &userquestions.Intent{
						Kind:    userquestions.IntentPlanReview,
						Approve: "Ship it",
					},
				},
			},
		},
	)
	requireQuestionCode(t, err, userquestions.CodeBadIntent)
	if len(providerValue.requests) != 0 {
		t.Fatalf("provider calls after invalid asks = %d", len(providerValue.requests))
	}
}

func TestAskRequiresExactLiveRuntimeRoot(t *testing.T) {
	t.Parallel()
	state := newQuestionsFixture(t, true)
	providerValue := &recordingProvider{
		answer: userquestions.Answer{
			Answers: []userquestions.AnswerItem{
				{
					ID:       "confirm",
					Selected: []string{"yes"},
				},
			},
		},
	}
	if _, err := state.questions.RegisterProvider(providerValue); err != nil {
		t.Fatal(err)
	}
	rootSubject := newQuestionSubject(t, state, "root", "")
	childSubject := newQuestionSubject(t, state, "child", session.OriginSubagent)
	if err := state.registry.enter(rootSubject); err != nil {
		t.Fatal(err)
	}
	if err := state.registry.enter(childSubject); err != nil {
		t.Fatal(err)
	}
	questions := []userquestions.Question{
		{
			ID:       "confirm",
			Question: "Proceed?",
		},
	}
	if _, err := state.questions.Ask(
		context.Background(),
		userquestions.Request{
			Questions: questions,
			Subject:   rootSubject,
		},
	); err != nil {
		t.Fatal(err)
	}
	_, err := state.questions.Ask(
		context.Background(),
		userquestions.Request{
			Questions: questions,
			Subject:   childSubject,
		},
	)
	requireQuestionCode(t, err, userquestions.CodeDelegatedCaller)
	staleSubject := newQuestionSubject(t, state, "root", "")
	_, err = state.questions.Ask(
		context.Background(),
		userquestions.Request{
			Questions: questions,
			Subject:   staleSubject,
		},
	)
	requireQuestionCode(t, err, userquestions.CodeCallerNotLive)
	if len(providerValue.requests) != 1 || providerValue.requests[0].Subject != rootSubject {
		t.Fatalf("provider requests = %#v", providerValue.requests)
	}

	unattested := newQuestionsFixture(t, false)
	if _, err = unattested.questions.RegisterProvider(&recordingProvider{}); err != nil {
		t.Fatal(err)
	}
	_, err = unattested.questions.Ask(
		context.Background(),
		userquestions.Request{
			Questions: questions,
			Subject:   rootSubject,
		},
	)
	requireQuestionCode(t, err, userquestions.CodeCallerNotLive)
}
