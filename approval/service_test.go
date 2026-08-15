package approval_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	agentcore "github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/approval"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/systemprompt"
)

type approvalFixturePlugin struct {
	settings approval.Config
	fixture  *approvalFixture
}

type approvalFixture struct {
	engine        *plugin.Runtime
	pluginScope   *plugin.Scope
	promptService systemprompt.SystemPrompt
	serviceValue  approval.Approval
}

func (*approvalFixturePlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "approval-fixture",
		Provides: []plugin.ServiceRef{
			systemprompt.Service.Ref(), approval.Service.Ref(),
		},
	}
}

func (instance *approvalFixturePlugin) Apply(requestContext context.Context, pluginScope *plugin.Scope) error {
	promptSettings, err := systemprompt.ValidateConfig(systemprompt.Config{})
	if err != nil {
		return err
	}
	promptService, err := systemprompt.New(requestContext, pluginScope, promptSettings)
	if err != nil {
		return err
	}
	approvalSettings, err := approval.ValidateConfig(instance.settings)
	if err != nil {
		return err
	}
	serviceValue, err := approval.New(
		requestContext, pluginScope, promptService, approvalSettings, approval.RuntimeOptions{},
	)
	if err != nil {
		return err
	}
	if _, err := plugin.Provide(pluginScope, systemprompt.Service, promptService); err != nil {
		return err
	}
	if _, err := plugin.Provide(pluginScope, approval.Service, serviceValue); err != nil {
		return err
	}
	instance.fixture.pluginScope = pluginScope
	instance.fixture.promptService = promptService
	instance.fixture.serviceValue = serviceValue
	return nil
}

type fakeSubject struct {
	identifier   session.SessionID
	conversation *session.Session
	agentScope   *plugin.Scope

	mu       sync.Mutex
	injected []llm.UserMessage
}

func (subject *fakeSubject) ID() session.SessionID                           { return subject.identifier }
func (*fakeSubject) OptionsValue() agentcore.Options                         { return agentcore.Options{} }
func (subject *fakeSubject) SessionValue() *session.Session                  { return subject.conversation }
func (*fakeSubject) InboxValue() *agentcore.Inbox                            { return nil }
func (*fakeSubject) StatusValue() agentcore.Status                           { return agentcore.StatusIdle }
func (subject *fakeSubject) ScopeValue() *plugin.Scope                       { return subject.agentScope }
func (*fakeSubject) Cancel(agentcore.CancelCause, agentcore.CancelOptions)   {}
func (*fakeSubject) WhenIdle(context.Context) error                          { return nil }
func (*fakeSubject) Send(llm.UserMessage, agentcore.InboxTarget, bool) error { return nil }
func (*fakeSubject) Followup(llm.UserMessage) error                          { return nil }
func (*fakeSubject) Steer(llm.UserMessage) error                             { return nil }
func (subject *fakeSubject) Inject(messageValue llm.UserMessage) error {
	subject.mu.Lock()
	subject.injected = append(subject.injected, messageValue)
	subject.mu.Unlock()
	return nil
}
func (*fakeSubject) RunMaintenance(requestContext context.Context, task agentcore.MaintenanceTask) error {
	return task.Run(requestContext)
}

func newApprovalFixture(t *testing.T, settings approval.Config) *approvalFixture {
	t.Helper()
	state := &approvalFixture{engine: plugin.NewRuntime()}
	if _, err := state.engine.Load(context.Background(), &approvalFixturePlugin{settings: settings, fixture: state}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := state.engine.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return state
}

func newSubject(t *testing.T, pluginScope *plugin.Scope, identifier session.SessionID) *fakeSubject {
	t.Helper()
	agentScope, _, err := pluginScope.Child(string(identifier))
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := session.New(identifier, session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return &fakeSubject{identifier: identifier, conversation: conversation, agentScope: agentScope}
}

func openTurn(t *testing.T, conversation *session.Session) {
	t.Helper()
	if _, err := session.Append(conversation, session.TurnStarted, session.TurnStart{Turn: 1}); err != nil {
		t.Fatal(err)
	}
}

func TestRequestRequiresOpenTurnAndPairsAudit(t *testing.T) {
	t.Parallel()
	state := newApprovalFixture(t, approval.Config{})
	subject := newSubject(t, state.pluginScope, "approval-audit")

	if _, err := state.serviceValue.Request(context.Background(), approval.Request{
		Subject: subject, ToolName: "echo",
	}); err == nil || !strings.Contains(err.Error(), "outside an open turn") {
		t.Fatalf("idle request error = %v", err)
	}
	if len(subject.conversation.Events()) != 0 {
		t.Fatal("idle request appended an audit event")
	}

	openTurn(t, subject.conversation)
	callIdentifier := llm.CallID("call-1")
	reasonText := "hook says ask"
	outcome, err := state.serviceValue.Request(context.Background(), approval.Request{
		Subject: subject, ToolName: "echo", CallID: &callIdentifier, Reason: &reasonText,
	})
	if err != nil {
		t.Fatal(err)
	}
	if outcome != approval.OutcomeUnavailable {
		t.Fatalf("outcome = %q", outcome)
	}
	entries := subject.conversation.Events()
	if got := []string{entries[1].Type, entries[2].Type}; !reflect.DeepEqual(got, []string{
		approval.AskedEventName, approval.DecidedEventName,
	}) {
		t.Fatalf("audit event types = %#v", got)
	}
	var asked approval.Asked
	var decided approval.Decided
	if err := json.Unmarshal(entries[1].Data, &asked); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(entries[2].Data, &decided); err != nil {
		t.Fatal(err)
	}
	if asked.ID == "" || asked.ToolName != "echo" || asked.CallID == nil || *asked.CallID != callIdentifier ||
		asked.Reason == nil || *asked.Reason != reasonText || decided.ID != asked.ID || decided.Outcome != outcome {
		t.Fatalf("audit pair = (%#v, %#v)", asked, decided)
	}
}

func TestRequestUsesMatchingScopedAnswererAndContainsFailure(t *testing.T) {
	t.Parallel()
	state := newApprovalFixture(t, approval.Config{})
	subject := newSubject(t, state.pluginScope, "approval-scoped")
	foreign := newSubject(t, state.pluginScope, "approval-foreign")
	openTurn(t, subject.conversation)
	heard := []string{}
	if _, err := approval.OnRequest(state.pluginScope, func(_ context.Context, _ approval.Request, downstream approval.RequestNext) (approval.Outcome, error) {
		heard = append(heard, "global")
		return downstream(context.Background())
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := approval.OnRequest(subject.agentScope, func(context.Context, approval.Request, approval.RequestNext) (approval.Outcome, error) {
		heard = append(heard, "matching")
		return approval.OutcomeAllowedOnce, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := approval.OnRequest(foreign.agentScope, func(context.Context, approval.Request, approval.RequestNext) (approval.Outcome, error) {
		heard = append(heard, "foreign")
		return approval.OutcomeRejected, nil
	}); err != nil {
		t.Fatal(err)
	}
	outcome, err := state.serviceValue.Request(context.Background(), approval.Request{Subject: subject, ToolName: "echo"})
	if err != nil {
		t.Fatal(err)
	}
	if outcome != approval.OutcomeAllowedOnce || !reflect.DeepEqual(heard, []string{"global", "matching"}) {
		t.Fatalf("scoped decision = (%q, %#v)", outcome, heard)
	}

	broken := newSubject(t, state.pluginScope, "approval-broken")
	openTurn(t, broken.conversation)
	if _, err := approval.OnRequest(broken.agentScope, func(context.Context, approval.Request, approval.RequestNext) (approval.Outcome, error) {
		return "rogue", errors.New("answerer failed")
	}); err != nil {
		t.Fatal(err)
	}
	outcome, err = state.serviceValue.Request(context.Background(), approval.Request{Subject: broken, ToolName: "echo"})
	if err != nil {
		t.Fatal(err)
	}
	if outcome != approval.OutcomeUnavailable {
		t.Fatalf("contained outcome = %q", outcome)
	}
}

func TestPolicyFoldControlsDispatchAndPromptContext(t *testing.T) {
	t.Parallel()
	state := newApprovalFixture(t, approval.Config{})
	subject := newSubject(t, state.pluginScope, "approval-policy")
	openTurn(t, subject.conversation)
	answererCalls := 0
	if _, err := approval.OnRequest(subject.agentScope, func(context.Context, approval.Request, approval.RequestNext) (approval.Outcome, error) {
		answererCalls++
		return approval.OutcomeAllowedOnce, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := state.serviceValue.SetPolicy(context.Background(), subject, approval.PolicyNever); err != nil {
		t.Fatal(err)
	}
	outcome, err := state.serviceValue.Request(context.Background(), approval.Request{Subject: subject, ToolName: "echo"})
	if err != nil {
		t.Fatal(err)
	}
	if outcome != approval.OutcomeRejected || answererCalls != 0 {
		t.Fatalf("never policy decision = (%q, calls %d)", outcome, answererCalls)
	}
	subject.mu.Lock()
	injectedCount := len(subject.injected)
	subject.mu.Unlock()
	if injectedCount != 1 {
		t.Fatalf("injected policy notices = %d", injectedCount)
	}
	assembled, err := state.promptService.Assemble(context.Background(), systemprompt.AssembleContext{
		Scope: subject.agentScope.Target(), Session: subject.conversation,
	})
	if err != nil {
		t.Fatal(err)
	}
	contextText, err := systemprompt.RenderContextSnapshot(assembled)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(contextText, "Approval prompts are disabled in this session") {
		t.Fatalf("policy context = %q", contextText)
	}
	bare, err := state.promptService.Assemble(context.Background(), systemprompt.AssembleContext{})
	if err != nil {
		t.Fatal(err)
	}
	bareText, err := systemprompt.RenderContextSnapshot(bare)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(bareText, "Approval policy") || strings.Contains(bareText, "Approval prompts") {
		t.Fatalf("bare context leaked policy = %q", bareText)
	}
}

func TestRequestCancellationWinsAndAppendsOneDecision(t *testing.T) {
	t.Parallel()
	state := newApprovalFixture(t, approval.Config{})
	subject := newSubject(t, state.pluginScope, "approval-cancel")
	openTurn(t, subject.conversation)
	started := make(chan struct{})
	releaseAnswer := make(chan struct{})
	if _, err := approval.OnRequest(subject.agentScope, func(context.Context, approval.Request, approval.RequestNext) (approval.Outcome, error) {
		close(started)
		<-releaseAnswer
		return approval.OutcomeAllowedOnce, nil
	}); err != nil {
		t.Fatal(err)
	}
	requestContext, cancelRequest := context.WithCancel(context.Background())
	settled := make(chan approval.Outcome, 1)
	failed := make(chan error, 1)
	go func() {
		outcome, err := state.serviceValue.Request(requestContext, approval.Request{Subject: subject, ToolName: "echo"})
		if err != nil {
			failed <- err
			return
		}
		settled <- outcome
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("approval answerer did not start")
	}
	cancelRequest()
	select {
	case err := <-failed:
		t.Fatal(err)
	case outcome := <-settled:
		if outcome != approval.OutcomeCancelled {
			t.Fatalf("cancelled outcome = %q", outcome)
		}
	case <-time.After(time.Second):
		t.Fatal("approval cancellation did not settle")
	}
	close(releaseAnswer)
	time.Sleep(10 * time.Millisecond)
	decisions := 0
	for _, entry := range subject.conversation.Events() {
		if entry.Type == approval.DecidedEventName {
			decisions++
		}
	}
	if decisions != 1 {
		t.Fatalf("decided events = %d", decisions)
	}
}
