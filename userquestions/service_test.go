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

type questionsFixturePlugin struct {
	fixture *questionsFixture
}

type questionsFixture struct {
	engine        *plugin.Runtime
	pluginScope   *plugin.Scope
	agentRegistry agentcore.Registry
}

func (*questionsFixturePlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{Name: "user-questions-fixture", Provides: []plugin.ServiceRef{agentcore.Service.Ref()}}
}

func (instance *questionsFixturePlugin) Apply(_ context.Context, pluginScope *plugin.Scope) error {
	agentRegistry, err := agentcore.NewRegistry(pluginScope, agentcore.RegistryOptions{})
	if err != nil {
		return err
	}
	if _, err := plugin.Provide(pluginScope, agentcore.Service, agentRegistry); err != nil {
		return err
	}
	instance.fixture.pluginScope = pluginScope
	instance.fixture.agentRegistry = agentRegistry
	return nil
}

type questionSubject struct {
	identifier   session.SessionID
	conversation *session.Session
	agentScope   *plugin.Scope
}

func (subject *questionSubject) ID() session.SessionID                           { return subject.identifier }
func (*questionSubject) OptionsValue() agentcore.Options                         { return agentcore.Options{} }
func (subject *questionSubject) SessionValue() *session.Session                  { return subject.conversation }
func (*questionSubject) InboxValue() *agentcore.Inbox                            { return nil }
func (*questionSubject) StatusValue() agentcore.Status                           { return agentcore.StatusIdle }
func (subject *questionSubject) ScopeValue() *plugin.Scope                       { return subject.agentScope }
func (*questionSubject) Cancel(agentcore.CancelCause, agentcore.CancelOptions)   {}
func (*questionSubject) WhenIdle(context.Context) error                          { return nil }
func (*questionSubject) Send(llm.UserMessage, agentcore.InboxTarget, bool) error { return nil }
func (*questionSubject) Followup(llm.UserMessage) error                          { return nil }
func (*questionSubject) Steer(llm.UserMessage) error                             { return nil }
func (*questionSubject) Inject(llm.UserMessage) error                            { return nil }
func (*questionSubject) RunMaintenance(requestContext context.Context, task agentcore.MaintenanceTask) error {
	return task.Run(requestContext)
}

type recordingProvider struct {
	requests []userquestions.Request
	answer   userquestions.Answer
	err      error
}

func (recorder *recordingProvider) Ask(_ context.Context, questionRequest userquestions.Request) (userquestions.Answer, error) {
	recorder.requests = append(recorder.requests, questionRequest)
	return recorder.answer, recorder.err
}

func newQuestionsFixture(t *testing.T) *questionsFixture {
	t.Helper()
	state := &questionsFixture{engine: plugin.NewRuntime()}
	if _, err := state.engine.Load(context.Background(), &questionsFixturePlugin{fixture: state}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := state.engine.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return state
}

func newQuestionSubject(
	t *testing.T,
	pluginScope *plugin.Scope,
	label string,
	identifier session.SessionID,
) *questionSubject {
	t.Helper()
	agentScope, _, err := pluginScope.Child(label)
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := session.New(identifier, session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return &questionSubject{identifier: identifier, conversation: conversation, agentScope: agentScope}
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
	state := newQuestionsFixture(t)
	serviceValue := userquestions.New(nil)
	answerText := "because it is reproducible"
	providerValue := &recordingProvider{answer: userquestions.Answer{Answers: []userquestions.AnswerItem{{
		ID: "pkg", Selected: []string{"pnpm"}, Custom: &answerText,
	}}}}
	release, err := serviceValue.RegisterProvider(context.Background(), state.pluginScope, providerValue)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := serviceValue.RegisterProvider(context.Background(), state.pluginScope, &recordingProvider{}); err == nil {
		t.Fatal("duplicate provider registration succeeded")
	} else {
		requireQuestionCode(t, err, userquestions.CodeDuplicate)
	}
	questions := []userquestions.Question{{ID: "pkg", Question: "Which package manager?"}}
	answerValue, err := serviceValue.Ask(context.Background(), userquestions.Request{Questions: questions})
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
	if err := release(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := release(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, err = serviceValue.Ask(context.Background(), userquestions.Request{Questions: questions})
	requireQuestionCode(t, err, userquestions.CodeNoProvider)
}

func TestAskRejectsInvalidInputsBeforeProvider(t *testing.T) {
	t.Parallel()
	state := newQuestionsFixture(t)
	serviceValue := userquestions.New(nil)
	providerValue := &recordingProvider{}
	if _, err := serviceValue.RegisterProvider(context.Background(), state.pluginScope, providerValue); err != nil {
		t.Fatal(err)
	}
	abortedContext, cancelRequest := context.WithCancel(context.Background())
	cancelRequest()
	_, err := serviceValue.Ask(abortedContext, userquestions.Request{
		Questions: []userquestions.Question{{ID: "confirm", Question: "Proceed?"}},
	})
	requireQuestionCode(t, err, userquestions.CodeAborted)
	_, err = serviceValue.Ask(context.Background(), userquestions.Request{})
	requireQuestionCode(t, err, userquestions.CodeEmptyQuestions)
	options := []userquestions.Option{{Label: "Approve"}}
	detail := "# Plan"
	_, err = serviceValue.Ask(context.Background(), userquestions.Request{Questions: []userquestions.Question{{
		ID: "plan", Question: "Approve?", Detail: &detail, Options: &options,
		Intent: &userquestions.Intent{Kind: userquestions.IntentPlanReview, Approve: "Ship it"},
	}}})
	requireQuestionCode(t, err, userquestions.CodeBadIntent)
	if len(providerValue.requests) != 0 {
		t.Fatalf("provider calls after invalid asks = %d", len(providerValue.requests))
	}
}

func TestAskRequiresExactLiveRuntimeRoot(t *testing.T) {
	t.Parallel()
	state := newQuestionsFixture(t)
	serviceValue := userquestions.New(userquestions.AgentRegistryResolverFunc(func() (agentcore.Registry, bool) {
		return state.agentRegistry, true
	}))
	providerValue := &recordingProvider{answer: userquestions.Answer{Answers: []userquestions.AnswerItem{{
		ID: "confirm", Selected: []string{"yes"},
	}}}}
	if _, err := serviceValue.RegisterProvider(context.Background(), state.pluginScope, providerValue); err != nil {
		t.Fatal(err)
	}
	rootSubject := newQuestionSubject(t, state.pluginScope, "root-scope", "root")
	childSubject := newQuestionSubject(t, state.pluginScope, "child-scope", "child")
	if _, err := state.agentRegistry.Enter(rootSubject, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := state.agentRegistry.Enter(childSubject, rootSubject); err != nil {
		t.Fatal(err)
	}
	questions := []userquestions.Question{{ID: "confirm", Question: "Proceed?"}}
	if _, err := serviceValue.Ask(context.Background(), userquestions.Request{
		Questions: questions, Subject: rootSubject,
	}); err != nil {
		t.Fatal(err)
	}
	_, err := serviceValue.Ask(context.Background(), userquestions.Request{
		Questions: questions, Subject: childSubject,
	})
	requireQuestionCode(t, err, userquestions.CodeDelegatedCaller)
	staleSubject := newQuestionSubject(t, state.pluginScope, "stale-scope", "root")
	_, err = serviceValue.Ask(context.Background(), userquestions.Request{
		Questions: questions, Subject: staleSubject,
	})
	requireQuestionCode(t, err, userquestions.CodeCallerNotLive)
	if len(providerValue.requests) != 1 || providerValue.requests[0].Subject != rootSubject {
		t.Fatalf("provider requests = %#v", providerValue.requests)
	}

	unattestedService := userquestions.New(userquestions.AgentRegistryResolverFunc(func() (agentcore.Registry, bool) {
		return nil, false
	}))
	if _, err := unattestedService.RegisterProvider(context.Background(), state.pluginScope, &recordingProvider{}); err != nil {
		t.Fatal(err)
	}
	_, err = unattestedService.Ask(context.Background(), userquestions.Request{
		Questions: questions, Subject: rootSubject,
	})
	requireQuestionCode(t, err, userquestions.CodeCallerNotLive)
}
