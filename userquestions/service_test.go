package userquestions_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	agentcore "github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/userquestions"
)

type questionsFixture struct {
	engine         *plugin.Runtime
	registry       *agentcore.RegistryPlugin
	registryHandle plugin.Handle
	questions      *userquestions.QuestionService
}

type questionSubject struct {
	plugin.Base
	identifier   session.SessionID
	conversation *session.Session
	registry     agentcore.Registry
}

func (subject *questionSubject) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "userquestions-test-agent:" + string(subject.identifier),
		Provides: []plugin.ServiceType{
			plugin.ServiceOf[agentcore.Agent](),
		},
	}
}

func (*questionSubject) Apply(requestContext context.Context) error {
	return requestContext.Err()
}

func (subject *questionSubject) Dispose(requestContext context.Context) error {
	if subject.registry == nil {
		return nil
	}
	return subject.registry.Remove(requestContext, subject)
}

func (subject *questionSubject) ID() session.SessionID {
	return subject.identifier
}

func (*questionSubject) OptionsValue() agentcore.Options {
	return agentcore.Options{}
}

func (subject *questionSubject) SessionValue() *session.Session {
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
	task agentcore.MaintenanceTask,
) error {
	return task.Run(requestContext)
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
		engine:    plugin.NewRuntime(plugin.RuntimeSettings{}),
		questions: userquestions.New(),
	}
	instances := []plugin.Plugin{state.questions}
	if withRegistry {
		state.registry = agentcore.NewRegistry(agentcore.RegistryOptions{})
		instances = []plugin.Plugin{
			state.registry,
			state.questions,
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
) *questionSubject {
	t.Helper()
	conversation, err := session.New(identifier, session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	subject := &questionSubject{
		identifier:   identifier,
		conversation: conversation,
		registry:     state.registry,
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
	rootSubject := newQuestionSubject(t, state, "root")
	childSubject := newQuestionSubject(t, state, "child")
	if err := state.registry.Enter(rootSubject, nil); err != nil {
		t.Fatal(err)
	}
	if err := state.registry.Enter(childSubject, rootSubject); err != nil {
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
	staleSubject := newQuestionSubject(t, state, "root")
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
