package interaction

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/gorenx/goren/apiproxy/rpc"
	"github.com/gorenx/goren/apiproxy/stream"
	"github.com/gorenx/goren/approval"
	"github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/userquestions"
)

// frameRecorder observes publication only. Reconnect and transport ordering
// require Broker integration tests and are not simulated by this fixture.
type frameRecorder struct {
	requested chan stream.Request[stream.MuxFrame]
	resolved  chan stream.Request[stream.MuxFrame]
}

func (sink *frameRecorder) PublishPending(id connection.RPCID, payload stream.MuxFrame) error {
	if _, err := stream.EncodeMuxFrame(payload); err != nil {
		return err
	}
	sink.requested <- stream.Request[stream.MuxFrame]{
		RPCID:   id,
		Payload: payload,
	}
	return nil
}
func (sink *frameRecorder) ResolvePending(id connection.RPCID, payload stream.MuxFrame) error {
	if _, err := stream.EncodeMuxFrame(payload); err != nil {
		return err
	}
	sink.resolved <- stream.Request[stream.MuxFrame]{
		RPCID:   id,
		Payload: payload,
	}
	return nil
}
func gatewayFixture(t *testing.T) (*Gateway, *rpc.Catalog, *frameRecorder) {
	t.Helper()
	methods := rpc.NewCatalog()
	sink := &frameRecorder{
		requested: make(chan stream.Request[stream.MuxFrame], 8),
		resolved:  make(chan stream.Request[stream.MuxFrame], 8),
	}
	owner, err := NewGateway(Dependencies{
		Methods: methods,
		Frames:  sink,
	}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := owner.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return owner, methods, sink
}
func receive[T any](t *testing.T, arrivals <-chan T) T {
	t.Helper()
	select {
	case value := <-arrivals:
		return value
	case <-time.After(time.Second):
		t.Fatal("result timed out")
		var zero T
		return zero
	}
}
func answer(t *testing.T, methods *rpc.Catalog, id connection.RPCID, payload string) connection.RPCReceipt {
	t.Helper()
	receipt, err := methods.Respond(context.Background(), connection.ClientResponse{
		RPCID: id,
		Result: connection.RPCResult{
			OK:    true,
			Value: json.RawMessage(payload),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}
func TestApprovalCatalogValidationAndCancellation(t *testing.T) {
	owner, methods, sink := gatewayFixture(t)
	finished := make(chan approval.Outcome, 1)
	failures := make(chan error, 1)
	input := approval.Question{
		ID:        "approval-1",
		SessionID: "session-1",
		ToolName:  "bash",
	}
	go func() {
		decision, err := owner.Answer(context.Background(), input)
		finished <- decision
		failures <- err
	}()
	frame := receive(t, sink.requested)
	if frame.Payload.(ApprovalRequestedFrame).ApprovalID != "approval-1" {
		t.Fatal("lost approval identity")
	}
	if _, err := owner.Answer(context.Background(), input); err == nil {
		t.Fatal("duplicate request accepted")
	}
	invalid := answer(t, methods, frame.RPCID, `{"sessionId":"session-1","approvalId":"wrong","outcome":"allowed-once"}`)
	if invalid.Accepted || invalid.Reason != connection.ReceiptBadResponse {
		t.Fatalf("invalid: %+v", invalid)
	}
	accepted := answer(t, methods, frame.RPCID, `{"sessionId":"session-1","approvalId":"approval-1","outcome":"allowed-once"}`)
	if !accepted.Accepted {
		t.Fatalf("valid: %+v", accepted)
	}
	if receive(t, finished) != approval.OutcomeAllowedOnce || receive(t, failures) != nil {
		t.Fatal("wrong approval result")
	}
	resolved := receive(t, sink.resolved)
	if resolved.RPCID != frame.RPCID || resolved.Payload.(ApprovalResolvedFrame).Outcome != ApprovalAllowedOnce {
		t.Fatal("wrong terminal frame")
	}
	if duplicate := answer(t, methods, frame.RPCID, `{}`); duplicate.Reason != connection.ReceiptNotPending {
		t.Fatalf("duplicate: %+v", duplicate)
	}
	ctx, cancel := context.WithCancel(context.Background())
	input.ID = "approval-2"
	go func() { decision, err := owner.Answer(ctx, input); finished <- decision; failures <- err }()
	receive(t, sink.requested)
	cancel()
	if receive(t, finished) != approval.OutcomeCancelled || receive(t, failures) != nil {
		t.Fatal("cancellation not delivered")
	}
	if receive(t, sink.resolved).Payload.(ApprovalResolvedFrame).Outcome != ApprovalCancelled {
		t.Fatal("missing cancellation frame")
	}
}
func TestQuestionCatalogRejectsInvalidAnswerAndSettles(t *testing.T) {
	owner, methods, sink := gatewayFixture(t)
	answers := make(chan userquestions.Answer, 1)
	failures := make(chan error, 1)
	choices := []userquestions.Option{{
		Label: "Code",
	}, {
		Label: "Docs",
	}}
	go func() {
		result, err := owner.Ask(context.Background(), userquestions.Request{
			SessionID: "session-1",
			Questions: []userquestions.Question{{
				ID:       "target",
				Question: "Choose",
				Options:  &choices,
			}},
		})
		answers <- result
		failures <- err
	}()
	requested := receive(t, sink.requested)
	for _, payload := range []string{
		`{"sessionId":"wrong","answer":{"answers":[{"id":"target","selected":["Code"]}]}}`,
		`{"sessionId":"session-1","answer":{"answers":[{"id":"target","selected":["Code"],"custom":"notes"}]}}`,
		`{"sessionId":"session-1","answer":{"answers":[{"id":"target","selected":["Unknown"]}]}}`,
	} {
		if receipt := answer(t, methods, requested.RPCID, payload); receipt.Accepted || receipt.Reason != connection.ReceiptBadResponse {
			t.Fatalf("invalid receipt: %+v", receipt)
		}
	}
	if receipt := answer(t, methods, requested.RPCID, `{"sessionId":"session-1","answer":{"answers":[{"id":"target","selected":[],"custom":"notes"}]}}`); !receipt.Accepted {
		t.Fatalf("valid receipt: %+v", receipt)
	}
	result := receive(t, answers)
	if receive(t, failures) != nil || len(result.Answers) != 1 || result.Answers[0].Custom == nil || *result.Answers[0].Custom != "notes" {
		t.Fatal("answer lost")
	}
	resolved := receive(t, sink.resolved).Payload.(QuestionResolvedFrame)
	if resolved.QuestionRPCID != requested.RPCID || resolved.Outcome != QuestionAnswered {
		t.Fatal("wrong question resolution")
	}
}
func TestCloseSettlesPendingInteractions(t *testing.T) {
	owner, methods, sink := gatewayFixture(t)
	decisions := make(chan approval.Outcome, 1)
	failures := make(chan error, 1)
	go func() {
		value, _ := owner.Answer(context.Background(), approval.Question{
			ID:        "approval",
			SessionID: "session",
			ToolName:  "bash",
		})
		decisions <- value
	}()
	go func() {
		_, err := owner.Ask(context.Background(), userquestions.Request{
			SessionID: "session",
			Questions: []userquestions.Question{{
				ID:       "q",
				Question: "Continue?",
			}},
		})
		failures <- err
	}()
	first := receive(t, sink.requested)
	second := receive(t, sink.requested)
	if err := owner.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if receive(t, decisions) != approval.OutcomeCancelled {
		t.Fatal("approval not cancelled")
	}
	var problem *userquestions.Error
	if failure := receive(t, failures); !errors.As(failure, &problem) || problem.Code != userquestions.CodeAborted {
		t.Fatalf("question close: %v", failure)
	}
	receive(t, sink.resolved)
	receive(t, sink.resolved)
	for _, pending := range []connection.RPCID{first.RPCID, second.RPCID} {
		if receipt := answer(t, methods, pending, `{}`); receipt.Reason != connection.ReceiptNotPending {
			t.Fatal("closed response still pending")
		}
	}
}

// blockedBroker holds terminal publication to expose concurrent Close semantics.
type blockedBroker struct {
	frameRecorder
	entered chan struct{}
	release chan struct{}
}

func (sink *blockedBroker) ResolvePending(id connection.RPCID, payload stream.MuxFrame) error {
	close(sink.entered)
	<-sink.release
	return sink.frameRecorder.ResolvePending(id, payload)
}
func TestConcurrentCloseWaitsForSettlement(t *testing.T) {
	sink := &blockedBroker{
		frameRecorder: frameRecorder{
			requested: make(chan stream.Request[stream.MuxFrame], 1),
			resolved:  make(chan stream.Request[stream.MuxFrame], 1),
		},
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	owner, err := NewGateway(Dependencies{
		Methods: rpc.NewCatalog(),
		Frames:  sink,
	}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	answered := make(chan error, 1)
	go func() {
		_, err := owner.Answer(context.Background(), approval.Question{
			ID:        "approval",
			SessionID: "session",
			ToolName:  "bash",
		})
		answered <- err
	}()
	receive(t, sink.requested)
	closed := make(chan error, 1)
	go func() { closed <- owner.Close(context.Background()) }()
	receive(t, sink.entered)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	failure := owner.Close(ctx)
	close(sink.release)
	if !errors.Is(failure, context.Canceled) {
		t.Errorf("concurrent Close reported completion before settlement: %v", failure)
	}
	if err := receive(t, closed); err != nil {
		t.Fatal(err)
	}
	if err := receive(t, answered); err != nil {
		t.Fatal(err)
	}
	if err := owner.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}
