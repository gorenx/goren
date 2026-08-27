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

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/approval"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/systemprompt"
)

type approvalFixture struct {
	runtimeEngine *plugin.Runtime
	prompts       *systemprompt.Registry
	promptHandle  plugin.Handle
	service       *approval.Service
}

type answererPlugin struct {
	plugin.Base
	name       string
	middleware plugin.WaterfallMiddleware[
		approval.DecisionRequest,
		approval.Decision,
	]
}

func (owner *answererPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: owner.name,
		Waterfalls: []plugin.WaterfallMiddlewareBinding{
			plugin.WaterfallOf[
				approval.DecisionRequest,
				approval.Decision,
			](owner),
		},
	}
}

func (*answererPlugin) Apply(context.Context) error {
	return nil
}

func (*answererPlugin) Dispose(context.Context) error {
	return nil
}

func (owner *answererPlugin) Intercept(
	requestContext context.Context,
	input approval.DecisionRequest,
	downstream plugin.WaterfallAction[
		approval.DecisionRequest,
		approval.Decision,
	],
) (approval.Decision, error) {
	return owner.middleware.Intercept(requestContext, input, downstream)
}

type fakeSubject struct {
	conversation session.Context

	mutex    sync.Mutex
	injected []agentmessage.UserMessage
}

func (subject *fakeSubject) SessionValue() session.Context {
	return subject.conversation
}

func (subject *fakeSubject) Inject(messageValue agentmessage.UserMessage) error {
	subject.mutex.Lock()
	subject.injected = append(subject.injected, messageValue)
	subject.mutex.Unlock()
	return nil
}

type answererFunc func(
	context.Context,
	approval.Request,
	plugin.WaterfallAction[approval.DecisionRequest, approval.Decision],
) (approval.Outcome, error)

func (operation answererFunc) Intercept(
	requestContext context.Context,
	request approval.DecisionRequest,
	downstream plugin.WaterfallAction[
		approval.DecisionRequest,
		approval.Decision,
	],
) (approval.Decision, error) {
	outcome, err := operation(
		requestContext,
		request.Request,
		downstream,
	)
	return approval.Decision{
		Outcome: outcome,
	}, err
}

func newApprovalFixture(
	testingContext *testing.T,
	settings approval.Config,
	middleware plugin.WaterfallMiddleware[
		approval.DecisionRequest,
		approval.Decision,
	],
) *approvalFixture {
	testingContext.Helper()
	promptSettings, err := systemprompt.ValidateConfig(systemprompt.Config{})
	if err != nil {
		testingContext.Fatal(err)
	}
	approvalSettings, err := approval.ValidateConfig(settings)
	if err != nil {
		testingContext.Fatal(err)
	}
	prompts := systemprompt.New(
		promptSettings,
		systemprompt.RegistryOptions{},
	)
	service := approval.New(approvalSettings)
	plugins := []plugin.Plugin{
		prompts,
		service,
	}
	if middleware != nil {
		plugins = append(plugins, &answererPlugin{
			name:       "root-approval-answerer",
			middleware: middleware,
		})
	}
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	handles, err := runtimeEngine.Start(
		context.Background(),
		plugins...,
	)
	if err != nil {
		testingContext.Fatal(err)
	}
	state := &approvalFixture{
		runtimeEngine: runtimeEngine,
		prompts:       prompts,
		promptHandle:  handles[0],
		service:       service,
	}
	testingContext.Cleanup(func() {
		if err := runtimeEngine.Shutdown(context.Background()); err != nil {
			testingContext.Error(err)
		}
	})
	return state
}

func (state *approvalFixture) mountOverlay(
	testingContext *testing.T,
	middleware plugin.WaterfallMiddleware[
		approval.DecisionRequest,
		approval.Decision,
	],
) *approval.Service {
	testingContext.Helper()
	promptOverlay := systemprompt.NewOverlay(systemprompt.RegistryOptions{})
	promptOverlayHandle, err := state.runtimeEngine.MountScopedChild(
		context.Background(),
		state.promptHandle,
		promptOverlay,
	)
	if err != nil {
		testingContext.Fatal(err)
	}
	parentHandle := promptOverlayHandle
	if middleware != nil {
		answererHandle, mountErr := state.runtimeEngine.MountScopedChild(
			context.Background(),
			promptOverlayHandle,
			&answererPlugin{
				name:       "scoped-approval-answerer",
				middleware: middleware,
			},
		)
		if mountErr != nil {
			testingContext.Fatal(mountErr)
		}
		parentHandle = answererHandle
	}
	approvalOverlay := approval.NewOverlay()
	if _, err := state.runtimeEngine.MountScopedChild(
		context.Background(),
		parentHandle,
		approvalOverlay,
	); err != nil {
		testingContext.Fatal(err)
	}
	return approvalOverlay
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
	state := newApprovalFixture(t, approval.Config{}, nil)
	subject := newSubject(t, "approval-audit")

	if _, err := state.service.Request(
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
	outcome, err := state.service.Request(
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

func TestRequestUsesOverlayAnswererAndContainsFailure(t *testing.T) {
	t.Parallel()
	heard := make([]string, 0)
	state := newApprovalFixture(t, approval.Config{}, answererFunc(func(
		requestContext context.Context,
		approvalInput approval.Request,
		downstream plugin.WaterfallAction[
			approval.DecisionRequest,
			approval.Decision,
		],
	) (approval.Outcome, error) {
		heard = append(heard, "global")
		decision, err := downstream.Execute(
			requestContext,
			approval.DecisionRequest{
				Request: approvalInput,
			},
		)
		return decision.Outcome, err
	}))
	matching := state.mountOverlay(t, answererFunc(func(
		context.Context,
		approval.Request,
		plugin.WaterfallAction[
			approval.DecisionRequest,
			approval.Decision,
		],
	) (approval.Outcome, error) {
		heard = append(heard, "matching")
		return approval.OutcomeAllowedOnce, nil
	}))
	_ = state.mountOverlay(t, answererFunc(func(
		context.Context,
		approval.Request,
		plugin.WaterfallAction[
			approval.DecisionRequest,
			approval.Decision,
		],
	) (approval.Outcome, error) {
		heard = append(heard, "foreign")
		return approval.OutcomeRejected, nil
	}))
	subject := newSubject(t, "approval-scoped")
	openTurn(t, subject.conversation)
	outcome, err := matching.Request(context.Background(), approval.Request{
		Subject:  subject,
		ToolName: "echo",
	})
	if err != nil {
		t.Fatal(err)
	}
	if outcome != approval.OutcomeAllowedOnce || !reflect.DeepEqual(
		heard,
		[]string{
			"global",
			"matching",
		},
	) {
		t.Fatalf("overlay decision = (%q, %#v)", outcome, heard)
	}

	broken := state.mountOverlay(t, answererFunc(func(
		context.Context,
		approval.Request,
		plugin.WaterfallAction[
			approval.DecisionRequest,
			approval.Decision,
		],
	) (approval.Outcome, error) {
		return "rogue", errors.New("answerer failed")
	}))
	brokenSubject := newSubject(t, "approval-broken")
	openTurn(t, brokenSubject.conversation)
	outcome, err = broken.Request(context.Background(), approval.Request{
		Subject:  brokenSubject,
		ToolName: "echo",
	})
	if err != nil {
		t.Fatal(err)
	}
	if outcome != approval.OutcomeUnavailable {
		t.Fatalf("contained outcome = %q", outcome)
	}
}

func TestPolicyFoldControlsDispatchAndPromptContext(t *testing.T) {
	t.Parallel()
	answererCalls := 0
	state := newApprovalFixture(t, approval.Config{}, answererFunc(func(
		context.Context,
		approval.Request,
		plugin.WaterfallAction[
			approval.DecisionRequest,
			approval.Decision,
		],
	) (approval.Outcome, error) {
		answererCalls++
		return approval.OutcomeAllowedOnce, nil
	}))
	subject := newSubject(t, "approval-policy")
	openTurn(t, subject.conversation)
	if err := state.service.SetPolicy(
		context.Background(),
		subject,
		approval.PolicyNever,
	); err != nil {
		t.Fatal(err)
	}
	outcome, err := state.service.Request(
		context.Background(),
		approval.Request{
			Subject:  subject,
			ToolName: "echo",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if outcome != approval.OutcomeRejected || answererCalls != 0 {
		t.Fatalf(
			"never policy decision = (%q, calls %d)",
			outcome,
			answererCalls,
		)
	}
	subject.mutex.Lock()
	injectedCount := len(subject.injected)
	subject.mutex.Unlock()
	if injectedCount != 1 {
		t.Fatalf("injected policy notices = %d", injectedCount)
	}
	assembled, err := state.prompts.Assemble(
		context.Background(),
		systemprompt.AssembleContext{
			Session: subject.conversation,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	contextText, err := systemprompt.RenderContextSnapshot(assembled)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(
		contextText,
		"Approval prompts are disabled in this session",
	) {
		t.Fatalf("policy context = %q", contextText)
	}
	bare, err := state.prompts.Assemble(
		context.Background(),
		systemprompt.AssembleContext{},
	)
	if err != nil {
		t.Fatal(err)
	}
	bareText, err := systemprompt.RenderContextSnapshot(bare)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(bareText, "Approval policy") ||
		strings.Contains(bareText, "Approval prompts") {
		t.Fatalf("bare context leaked policy = %q", bareText)
	}
}

func TestRequestCancellationWinsAndAppendsOneDecision(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	releaseAnswer := make(chan struct{})
	state := newApprovalFixture(t, approval.Config{}, answererFunc(func(
		context.Context,
		approval.Request,
		plugin.WaterfallAction[
			approval.DecisionRequest,
			approval.Decision,
		],
	) (approval.Outcome, error) {
		close(started)
		<-releaseAnswer
		return approval.OutcomeAllowedOnce, nil
	}))
	subject := newSubject(t, "approval-cancel")
	openTurn(t, subject.conversation)
	requestContext, cancelRequest := context.WithCancel(context.Background())
	settled := make(chan approval.Outcome, 1)
	failed := make(chan error, 1)
	go func() {
		outcome, err := state.service.Request(
			requestContext,
			approval.Request{
				Subject:  subject,
				ToolName: "echo",
			},
		)
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
