package apiproxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	agentcore "github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/approval"
	"github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/systemprompt"
	"github.com/gorenx/goren/userquestions"
)

type interactionFixturePlugin struct {
	state *interactionFixture
}

type interactionFixture struct {
	engine          *plugin.Runtime
	pluginScope     *plugin.Scope
	agents          agentcore.Registry
	approvalService approval.Approval
	questionService userquestions.UserQuestions
	methods         *Catalog
	frameHub        *liveFrameHub

	rpcMutex sync.Mutex
	nextRPC  int
}

func (*interactionFixturePlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{Name: "api-proxy-interaction-fixture"}
}

func (instance *interactionFixturePlugin) Apply(requestContext context.Context, pluginScope *plugin.Scope) error {
	promptSettings, err := systemprompt.ValidateConfig(systemprompt.Config{})
	if err != nil {
		return err
	}
	promptService, err := systemprompt.New(requestContext, pluginScope, promptSettings)
	if err != nil {
		return err
	}
	approvalSettings, err := approval.ValidateConfig(approval.Config{})
	if err != nil {
		return err
	}
	approvalService, err := approval.New(
		requestContext, pluginScope, promptService, approvalSettings, approval.RuntimeOptions{},
	)
	if err != nil {
		return err
	}
	agentRegistry, err := agentcore.NewRegistry(pluginScope, agentcore.RegistryOptions{})
	if err != nil {
		return err
	}
	questionService := userquestions.New(userquestions.AgentRegistryResolverFunc(func() (agentcore.Registry, bool) {
		return agentRegistry, true
	}))
	methods := NewCatalog()
	frameHub := newLiveFrameHub(instance.state.mintRPC)
	if _, err := NewInteractionGateway(
		requestContext,
		pluginScope,
		InteractionGatewayDependencies{
			Methods: methods, Frames: frameHub, UserQuestions: questionService,
		},
		InteractionGatewayOptions{NewRPCID: instance.state.mintRPC},
	); err != nil {
		return err
	}
	instance.state.pluginScope = pluginScope
	instance.state.agents = agentRegistry
	instance.state.approvalService = approvalService
	instance.state.questionService = questionService
	instance.state.methods = methods
	instance.state.frameHub = frameHub
	return nil
}

func (state *interactionFixture) mintRPC() (connection.RPCID, error) {
	state.rpcMutex.Lock()
	defer state.rpcMutex.Unlock()
	state.nextRPC++
	return connection.RPCID(fmt.Sprintf("interaction-rpc-%d", state.nextRPC)), nil
}

type interactionSubject struct {
	identifier   session.SessionID
	conversation *session.Session
	agentScope   *plugin.Scope
}

func (subject *interactionSubject) ID() session.SessionID { return subject.identifier }
func (*interactionSubject) OptionsValue() agentcore.Options {
	return agentcore.Options{}
}
func (subject *interactionSubject) SessionValue() *session.Session { return subject.conversation }
func (*interactionSubject) InboxValue() *agentcore.Inbox           { return nil }
func (*interactionSubject) StatusValue() agentcore.Status          { return agentcore.StatusIdle }
func (subject *interactionSubject) ScopeValue() *plugin.Scope      { return subject.agentScope }
func (*interactionSubject) Cancel(agentcore.CancelCause, agentcore.CancelOptions) {
}
func (*interactionSubject) WhenIdle(context.Context) error { return nil }
func (*interactionSubject) Send(llm.UserMessage, agentcore.InboxTarget, bool) error {
	return nil
}
func (*interactionSubject) Followup(llm.UserMessage) error { return nil }
func (*interactionSubject) Steer(llm.UserMessage) error    { return nil }
func (*interactionSubject) Inject(llm.UserMessage) error   { return nil }
func (*interactionSubject) RunMaintenance(requestContext context.Context, task agentcore.MaintenanceTask) error {
	return task.Run(requestContext)
}

type capturedMux struct {
	streamContext context.Context
	cancel        context.CancelFunc
	received      chan StreamRequest[MuxFrame]
	done          chan error
}

func newInteractionFixture(t *testing.T) *interactionFixture {
	t.Helper()
	state := &interactionFixture{engine: plugin.NewRuntime()}
	if _, err := state.engine.Load(context.Background(), &interactionFixturePlugin{state: state}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := state.engine.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return state
}

func (state *interactionFixture) newSubject(t *testing.T, identifier session.SessionID) *interactionSubject {
	t.Helper()
	agentScope, _, err := state.pluginScope.Child(string(identifier))
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := session.New(identifier, session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	subject := &interactionSubject{identifier: identifier, conversation: conversation, agentScope: agentScope}
	if _, err := state.agents.Register(context.Background(), agentScope, subject, nil); err != nil {
		t.Fatal(err)
	}
	return subject
}

func (state *interactionFixture) openMux(t *testing.T, conversations ...*session.Session) *capturedMux {
	t.Helper()
	streamContext, cancelStream := context.WithCancel(context.Background())
	stream := &capturedMux{
		streamContext: streamContext, cancel: cancelStream,
		received: make(chan StreamRequest[MuxFrame], 32), done: make(chan error, 1),
	}
	go func() {
		stream.done <- state.frameHub.openMux(streamContext, conversations, func(envelope StreamRequest[MuxFrame]) error {
			stream.received <- envelope
			return nil
		})
	}()
	return stream
}

func (stream *capturedMux) stop(t *testing.T) {
	t.Helper()
	stream.cancel()
	select {
	case err := <-stream.done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("mux stream did not stop")
	}
}

func waitMuxKind[F MuxFrame](t *testing.T, stream *capturedMux, kind string) StreamRequest[F] {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		select {
		case envelope := <-stream.received:
			if envelope.Payload.frameType() != kind {
				continue
			}
			payloadValue, matched := envelope.Payload.(F)
			if !matched {
				t.Fatalf("mux payload %T does not match requested test type", envelope.Payload)
			}
			return StreamRequest[F]{RPCID: envelope.RPCID, Payload: payloadValue}
		case <-deadline:
			t.Fatalf("mux frame %q timed out", kind)
		}
	}
}

func successfulClientResponse(t *testing.T, correlationID connection.RPCID, value any) connection.ClientResponse {
	t.Helper()
	rpcResult, err := connection.Success(value)
	if err != nil {
		t.Fatal(err)
	}
	return connection.ClientResponse{Type: connection.ClientResponseType, RPCID: correlationID, Result: rpcResult}
}

func TestApprovalInteractionRoundTripValidationAndReconnectReplay(t *testing.T) {
	state := newInteractionFixture(t)
	subject := state.newSubject(t, "approval-session")
	if _, err := session.Append(subject.conversation, session.TurnStarted, session.TurnStart{Turn: 1}); err != nil {
		t.Fatal(err)
	}
	firstMux := state.openMux(t, subject.conversation)
	decisionChannel := make(chan approval.Outcome, 1)
	errorChannel := make(chan error, 1)
	reasonText := "sandbox escalation"
	go func() {
		decision, err := state.approvalService.Request(context.Background(), approval.Request{
			Subject: subject, ToolName: "bash", Reason: &reasonText,
		})
		decisionChannel <- decision
		errorChannel <- err
	}()
	requested := waitMuxKind[ApprovalRequestedFrame](t, firstMux, "approval/requested")
	if requested.Payload.SessionID != "approval-session" || requested.Payload.ApprovalID == "" ||
		requested.Payload.ToolName != "bash" || requested.Payload.Reason == nil || *requested.Payload.Reason != reasonText {
		t.Fatalf("requested approval = %#v", requested)
	}
	firstMux.stop(t)

	secondMux := state.openMux(t, subject.conversation)
	replayed := waitMuxKind[ApprovalRequestedFrame](t, secondMux, "approval/requested")
	if replayed.RPCID != requested.RPCID || !reflect.DeepEqual(replayed.Payload, requested.Payload) {
		t.Fatalf("replayed approval = %#v, want %#v", replayed, requested)
	}
	badReceipt, err := state.methods.Respond(context.Background(), successfulClientResponse(t, replayed.RPCID, struct {
		SessionID  SessionID         `json:"sessionId"`
		ApprovalID ApprovalRequestID `json:"approvalId"`
		Outcome    ApprovalOutcome   `json:"outcome"`
	}{SessionID: replayed.Payload.SessionID, ApprovalID: "wrong-id", Outcome: ApprovalRejected}))
	if err != nil || badReceipt.Accepted || badReceipt.Reason != connection.ReceiptBadResponse {
		t.Fatalf("mismatched approval receipt = (%#v, %v)", badReceipt, err)
	}
	accepted, err := state.methods.Respond(context.Background(), successfulClientResponse(t, replayed.RPCID, struct {
		SessionID  SessionID         `json:"sessionId"`
		ApprovalID ApprovalRequestID `json:"approvalId"`
		Outcome    ApprovalOutcome   `json:"outcome"`
	}{
		SessionID:  replayed.Payload.SessionID,
		ApprovalID: replayed.Payload.ApprovalID,
		Outcome:    ApprovalAllowedOnce,
	}))
	if err != nil || !accepted.Accepted {
		t.Fatalf("approval receipt = (%#v, %v)", accepted, err)
	}
	if err := <-errorChannel; err != nil {
		t.Fatal(err)
	}
	if decision := <-decisionChannel; decision != approval.OutcomeAllowedOnce {
		t.Fatalf("approval decision = %q", decision)
	}
	resolved := waitMuxKind[ApprovalResolvedFrame](t, secondMux, "approval/resolved")
	if resolved.Payload.ApprovalID != replayed.Payload.ApprovalID || resolved.Payload.Outcome != ApprovalAllowedOnce {
		t.Fatalf("resolved approval = %#v", resolved)
	}
	duplicate, err := state.methods.Respond(context.Background(), successfulClientResponse(t, replayed.RPCID, struct{}{}))
	if err != nil || duplicate.Accepted || duplicate.Reason != connection.ReceiptNotPending {
		t.Fatalf("duplicate approval receipt = (%#v, %v)", duplicate, err)
	}
	secondMux.stop(t)
}

func TestApprovalInteractionPairsParallelCallAuditAndCancels(t *testing.T) {
	state := newInteractionFixture(t)
	subject := state.newSubject(t, "parallel-approval")
	if _, err := session.Append(subject.conversation, session.TurnStarted, session.TurnStart{Turn: 1}); err != nil {
		t.Fatal(err)
	}
	stream := state.openMux(t, subject.conversation)
	callA := llm.CallID("call-a")
	callB := llm.CallID("call-b")
	contextA, cancelA := context.WithCancel(context.Background())
	decisions := make(chan approval.Outcome, 2)
	for _, input := range []struct {
		callContext context.Context
		callID      *llm.CallID
		toolName    string
	}{{contextA, &callA, "alpha"}, {context.Background(), &callB, "beta"}} {
		inputValue := input
		go func() {
			decision, _ := state.approvalService.Request(inputValue.callContext, approval.Request{
				Subject: subject, ToolName: inputValue.toolName, CallID: inputValue.callID,
			})
			decisions <- decision
		}()
	}
	requestedA := waitMuxKind[ApprovalRequestedFrame](t, stream, "approval/requested")
	requestedB := waitMuxKind[ApprovalRequestedFrame](t, stream, "approval/requested")
	byCall := map[string]StreamRequest[ApprovalRequestedFrame]{
		*requestedA.Payload.CallID: requestedA,
		*requestedB.Payload.CallID: requestedB,
	}
	if byCall["call-a"].Payload.ApprovalID == byCall["call-b"].Payload.ApprovalID {
		t.Fatal("parallel approvals shared an audit id")
	}
	auditByCall := make(map[string]ApprovalRequestID)
	for _, committed := range subject.conversation.Events() {
		if committed.Type != approval.AskedEventName {
			continue
		}
		var askedValue approval.Asked
		if err := json.Unmarshal(committed.Data, &askedValue); err != nil {
			t.Fatal(err)
		}
		if askedValue.CallID != nil {
			auditByCall[string(*askedValue.CallID)] = ApprovalRequestID(askedValue.ID)
		}
	}
	if byCall["call-a"].Payload.ApprovalID != auditByCall["call-a"] ||
		byCall["call-b"].Payload.ApprovalID != auditByCall["call-b"] {
		t.Fatalf("approval frames do not preserve call audit pairing: frames=%#v audit=%#v", byCall, auditByCall)
	}
	cancelA()
	resolved := waitMuxKind[ApprovalResolvedFrame](t, stream, "approval/resolved")
	if resolved.Payload.ApprovalID != byCall["call-a"].Payload.ApprovalID || resolved.Payload.Outcome != ApprovalCancelled {
		t.Fatalf("cancelled approval = %#v", resolved)
	}
	accepted, err := state.methods.Respond(context.Background(), successfulClientResponse(t, byCall["call-b"].RPCID, struct {
		SessionID  SessionID         `json:"sessionId"`
		ApprovalID ApprovalRequestID `json:"approvalId"`
		Outcome    ApprovalOutcome   `json:"outcome"`
	}{
		SessionID: "parallel-approval", ApprovalID: byCall["call-b"].Payload.ApprovalID, Outcome: ApprovalRejected,
	}))
	if err != nil || !accepted.Accepted {
		t.Fatalf("parallel approval receipt = (%#v, %v)", accepted, err)
	}
	firstDecision := <-decisions
	secondDecision := <-decisions
	if !((firstDecision == approval.OutcomeCancelled && secondDecision == approval.OutcomeRejected) ||
		(firstDecision == approval.OutcomeRejected && secondDecision == approval.OutcomeCancelled)) {
		t.Fatalf("parallel decisions = (%q, %q)", firstDecision, secondDecision)
	}
	stream.stop(t)
}

func TestQuestionInteractionValidatesAnswersAndClientCancellation(t *testing.T) {
	state := newInteractionFixture(t)
	subject := state.newSubject(t, "question-session")
	stream := state.openMux(t, subject.conversation)
	options := []userquestions.Option{{Label: "Code"}, {Label: "Docs"}}
	responseChannel := make(chan userquestions.Answer, 1)
	errorChannel := make(chan error, 1)
	go func() {
		answerValue, err := state.questionService.Ask(context.Background(), userquestions.Request{
			Subject: subject,
			Questions: []userquestions.Question{{
				ID: "target", Question: "Choose one target", Options: &options,
			}},
		})
		responseChannel <- answerValue
		errorChannel <- err
	}()
	requested := waitMuxKind[QuestionRequestedFrame](t, stream, "question/requested")
	stream.stop(t)
	stream = state.openMux(t, subject.conversation)
	replayed := waitMuxKind[QuestionRequestedFrame](t, stream, "question/requested")
	if replayed.RPCID != requested.RPCID || !reflect.DeepEqual(replayed.Payload, requested.Payload) {
		t.Fatalf("replayed question = %#v, want %#v", replayed, requested)
	}
	requested = replayed
	invalid := successfulClientResponse(t, requested.RPCID, map[string]any{
		"sessionId": "question-session",
		"answer": map[string]any{"answers": []any{map[string]any{
			"id": "target", "selected": []string{"Code"}, "custom": "Release notes",
		}}},
	})
	receipt, err := state.methods.Respond(context.Background(), invalid)
	if err != nil || receipt.Accepted || receipt.Reason != connection.ReceiptBadResponse {
		t.Fatalf("invalid question receipt = (%#v, %v)", receipt, err)
	}
	valid := successfulClientResponse(t, requested.RPCID, map[string]any{
		"sessionId": "question-session",
		"answer": map[string]any{"answers": []any{map[string]any{
			"id": "target", "selected": []string{}, "custom": "Release notes",
		}}},
	})
	receipt, err = state.methods.Respond(context.Background(), valid)
	if err != nil || !receipt.Accepted {
		t.Fatalf("question receipt = (%#v, %v)", receipt, err)
	}
	if err := <-errorChannel; err != nil {
		t.Fatal(err)
	}
	answerValue := <-responseChannel
	if len(answerValue.Answers) != 1 || answerValue.Answers[0].Custom == nil ||
		*answerValue.Answers[0].Custom != "Release notes" || len(answerValue.Answers[0].Selected) != 0 {
		t.Fatalf("question answer = %#v", answerValue)
	}
	resolved := waitMuxKind[QuestionResolvedFrame](t, stream, "question/resolved")
	if resolved.Payload.QuestionRPCID != requested.RPCID || resolved.Payload.Outcome != QuestionAnswered {
		t.Fatalf("question resolution = %#v", resolved)
	}

	cancelError := connection.RPCError{Code: connection.ErrorCancelled, Message: "cancelled", Details: json.RawMessage(`{}`)}
	cancelledChannel := make(chan error, 1)
	go func() {
		_, err := state.questionService.Ask(context.Background(), userquestions.Request{
			Subject: subject, Questions: []userquestions.Question{{ID: "again", Question: "Continue?"}},
		})
		cancelledChannel <- err
	}()
	secondRequest := waitMuxKind[QuestionRequestedFrame](t, stream, "question/requested")
	receipt, err = state.methods.Respond(context.Background(), connection.ClientResponse{
		Type: connection.ClientResponseType, RPCID: secondRequest.RPCID,
		Result: connection.Failure(cancelError),
	})
	if err != nil || !receipt.Accepted {
		t.Fatalf("question cancellation receipt = (%#v, %v)", receipt, err)
	}
	var questionProblem *userquestions.Error
	if err := <-cancelledChannel; !errors.As(err, &questionProblem) || questionProblem.Code != userquestions.CodeCancelled {
		t.Fatalf("question cancellation error = %#v", err)
	}
	cancelledFrame := waitMuxKind[QuestionResolvedFrame](t, stream, "question/resolved")
	if cancelledFrame.Payload.QuestionRPCID != secondRequest.RPCID || cancelledFrame.Payload.Outcome != QuestionCancelled {
		t.Fatalf("cancelled question frame = %#v", cancelledFrame)
	}
	stream.stop(t)
}

func TestInteractionGatewayShutdownSettlesPendingApprovalAndQuestion(t *testing.T) {
	state := newInteractionFixture(t)
	subject := state.newSubject(t, "interaction-shutdown")
	if _, err := session.Append(subject.conversation, session.TurnStarted, session.TurnStart{Turn: 1}); err != nil {
		t.Fatal(err)
	}
	stream := state.openMux(t, subject.conversation)
	approvalChannel := make(chan approval.Outcome, 1)
	approvalErrorChannel := make(chan error, 1)
	go func() {
		decision, err := state.approvalService.Request(context.Background(), approval.Request{
			Subject: subject, ToolName: "write",
		})
		approvalChannel <- decision
		approvalErrorChannel <- err
	}()
	approvalRequest := waitMuxKind[ApprovalRequestedFrame](t, stream, "approval/requested")
	questionErrorChannel := make(chan error, 1)
	go func() {
		_, err := state.questionService.Ask(context.Background(), userquestions.Request{
			Subject: subject, Questions: []userquestions.Question{{ID: "confirm", Question: "Continue?"}},
		})
		questionErrorChannel <- err
	}()
	questionRequest := waitMuxKind[QuestionRequestedFrame](t, stream, "question/requested")

	if err := state.engine.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-approvalErrorChannel; err != nil {
		t.Fatal(err)
	}
	if decision := <-approvalChannel; decision != approval.OutcomeCancelled {
		t.Fatalf("shutdown approval decision = %q", decision)
	}
	var questionProblem *userquestions.Error
	if err := <-questionErrorChannel; !errors.As(err, &questionProblem) || questionProblem.Code != userquestions.CodeAborted {
		t.Fatalf("shutdown question error = %#v", err)
	}

	questionResolved := waitMuxKind[QuestionResolvedFrame](t, stream, "question/resolved")
	approvalResolved := waitMuxKind[ApprovalResolvedFrame](t, stream, "approval/resolved")
	if questionResolved.Payload.QuestionRPCID != questionRequest.RPCID ||
		questionResolved.Payload.Outcome != QuestionCancelled {
		t.Fatalf("shutdown question resolution = %#v", questionResolved)
	}
	if approvalResolved.Payload.ApprovalID != approvalRequest.Payload.ApprovalID ||
		approvalResolved.Payload.Outcome != ApprovalCancelled {
		t.Fatalf("shutdown approval resolution = %#v", approvalResolved)
	}
	stream.stop(t)
}
