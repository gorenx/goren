package subagent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gorenx/goren/agentmessage"
)

func TestSubagentMessageSourcesCanonicalizeTheirForms(t *testing.T) {
	t.Parallel()
	coordinatorSnapshot, err := (CoordinatorSource{
		SenderSessionID: "parent",
	}).CloneSource()
	if err != nil {
		t.Fatal(err)
	}
	coordinatorValue := coordinatorSnapshot.(CoordinatorSource)
	if coordinatorValue.Kind != "coordinator" ||
		coordinatorValue.Form != agentmessage.ContextRelay {
		t.Fatalf("coordinator source = %#v", coordinatorValue)
	}

	reportSnapshot, err := (ReportSource{
		SenderSessionID: "child",
	}).CloneSource()
	if err != nil {
		t.Fatal(err)
	}
	reportValue := reportSnapshot.(ReportSource)
	if reportValue.Kind != "subagent-report" ||
		reportValue.Form != agentmessage.ContextRelay {
		t.Fatalf("report source = %#v", reportValue)
	}

	settledSource, err := (SettlementSource{
		Summary:         strings.Repeat("x", agentmessage.ContextSummaryMaxChars+10),
		SenderSessionID: "child",
	}).CloneSource()
	if err != nil {
		t.Fatal(err)
	}
	settledValue := settledSource.(SettlementSource)
	if settledValue.Kind != "subagent-settled" ||
		settledValue.Form != agentmessage.ContextNotice ||
		settledValue.Summary != agentmessage.BoundContextSummary(strings.Repeat("x", agentmessage.ContextSummaryMaxChars+10)) {
		t.Fatalf("settled source = %#v", settledValue)
	}

	deliverySnapshot, err := (Delivery{
		ParentSessionID: "parent",
		Turn:            3,
		FromSeq:         8,
		ThroughSeq:      14,
		Outcome:         "completed",
	}).CloneSource()
	if err != nil {
		t.Fatal(err)
	}
	deliveryValue := deliverySnapshot.(Delivery)
	if deliveryValue.Kind != "subagent-delivery" ||
		deliveryValue.Form != agentmessage.ContextRelay {
		t.Fatalf("delivery = %#v", deliveryValue)
	}
	messageValue, err := agentmessage.NewUserMessage(
		agentmessage.UserMessageInput{
			Content: []agentmessage.ContentBlock{
				agentmessage.NewTextBlock("interaction"),
			},
			Source: deliveryValue,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	rawValue, err := json.Marshal(messageValue)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := agentmessage.DecodeUserMessage(rawValue)
	if err != nil {
		t.Fatal(err)
	}
	restoredDelivery, err := DecodeDelivery(restored.SourceValue())
	if err != nil || restoredDelivery != deliveryValue {
		t.Fatalf("restored delivery = %#v, error = %v", restoredDelivery, err)
	}
}

func TestSubagentMessageSourcesRejectInvalidAuthorityData(t *testing.T) {
	t.Parallel()
	_, err := (CoordinatorSource{}).CloneSource()
	if err == nil {
		t.Fatal("empty coordinator sender accepted")
	}
	_, err = (ReportSource{
		Form:            agentmessage.ContextNotice,
		SenderSessionID: "child",
	}).CloneSource()
	if err == nil {
		t.Fatal("report notice form accepted")
	}
	_, err = (Delivery{
		ParentSessionID: "parent",
		Turn:            1,
		FromSeq:         9,
		ThroughSeq:      8,
		Outcome:         "completed",
	}).CloneSource()
	if err == nil {
		t.Fatal("invalid parent interaction range accepted")
	}
}
