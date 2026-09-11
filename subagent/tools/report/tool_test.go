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

func (record *reporterRecord) Report(_ context.Context, child session.SessionID, output string, delivery Delivery) (agentmessage.MessageID, error) {
	record.child = child
	record.output = output
	record.delivery = delivery
	return "message", nil
}

func TestReportUsesInvocationSessionAndConfiguredDelivery(t *testing.T) {
	reporter := &reporterRecord{}
	adapter, err := newReportTool(reporter, NextStep)
	if err != nil {
		t.Fatal(err)
	}
	value, err := adapter.Execute(json.RawMessage(`{"output":"review complete"}`), tools.ToolRunContext{
		Context: context.Background(),
		Execution: tools.Invocation{
			SessionID: "child",
		},
	})
	if err != nil || reporter.child != "child" || reporter.output != "review complete" ||
		reporter.delivery != NextStep || string(value) != `{"messageId":"message"}` {
		t.Fatalf("value = %s, reporter = %#v, error = %v", value, reporter, err)
	}
}

func TestReportHonorsCancellation(t *testing.T) {
	reporter := &reporterRecord{}
	adapter, err := newReportTool(reporter, Quiet)
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
	if !errors.Is(err, context.Canceled) || reporter.child != "" {
		t.Fatalf("reporter = %#v, error = %v", reporter, err)
	}
}
