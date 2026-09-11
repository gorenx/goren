package report

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/tools"
)

type reporterRecord struct {
	child    session.SessionID
	output   string
	delivery Delivery
}

func (record *reporterRecord) Report(_ context.Context, input Report) (agentmessage.MessageID, error) {
	record.child = input.ChildSessionID
	record.output = input.Output
	record.delivery = input.Delivery
	return "message", nil
}

func TestReportUsesInvocationSessionAndConfiguredDelivery(t *testing.T) {
	recorded := &reporterRecord{}
	adapter, err := newReportTool(recorded, NextStep)
	if err != nil {
		t.Fatal(err)
	}
	value, err := adapter.Execute(json.RawMessage(`{"output":"review complete"}`), tools.ToolRunContext{
		Context: context.Background(),
		Execution: tools.Invocation{
			SessionID: "child",
		},
	})
	if err != nil || recorded.child != "child" || recorded.output != "review complete" ||
		recorded.delivery != NextStep || string(value) != `{"messageId":"message"}` {
		t.Fatalf("value = %s, reporter = %#v, error = %v", value, recorded, err)
	}
}

func TestReportHonorsCancellation(t *testing.T) {
	recorded := &reporterRecord{}
	adapter, err := newReportTool(recorded, Quiet)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = adapter.Execute(json.RawMessage(`{"output":"ignored"}`), tools.ToolRunContext{
		Context: ctx,
		Execution: tools.Invocation{
			SessionID: "child",
		},
	})
	if !errors.Is(err, context.Canceled) || recorded.child != "" {
		t.Fatalf("reporter = %#v, error = %v", recorded, err)
	}
}
