package subagent

import (
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
}
