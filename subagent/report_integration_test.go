package subagent_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/subagent/report"
	subagenttool "github.com/gorenx/goren/subagent/tool"
	"github.com/gorenx/goren/tools"
)

func TestReportExtensionDeliversChildSelectedContentToParent(t *testing.T) {
	reportPlugin, reportErr := report.New(subagent.ReportQuiet)
	if reportErr != nil {
		t.Fatal(reportErr)
	}
	state, _, backend := newContinuableIntegrationFixture(
		t,
		[][]llm.StreamChunk{
			{
				llm.BlockEndChunk{
					Index: 0,
					Block: llm.ToolCallBlock{
						ID:        "report-call-1",
						Name:      "report",
						Arguments: `{"output":"selected report content"}`,
					},
				},
				llm.FinishChunk{
					Reason: llm.ToolCallsFinish{},
				},
			},
			continuableTextResponse("child final answer"),
			continuableTextResponse("parent acknowledged report and settlement"),
		},
		reportPlugin,
	)
	parentHandle := state.createParent(t)
	started := state.toolRuntime.Execute(
		context.Background(),
		tools.ToolExecutionInput{
			CallID:     "start-reporting-child",
			RootCallID: "start-reporting-child",
			Name:       subagenttool.DefaultToolName,
			Arguments: json.RawMessage(`{
  "description": "report selected result",
  "prompt": "Report the selected result and then finish."
}`),
			Subject: parentHandle.Subject,
		},
	)
	if started.Failed() {
		failure, _ := started.FailureDetail()
		t.Fatalf("continuable start failed: %#v", failure)
	}
	rawStart, found := started.SuccessValue()
	if !found {
		t.Fatal("continuable start returned no value")
	}
	var startResult struct {
		SubagentID string `json:"subagentId"`
	}
	if decodeErr := json.Unmarshal(rawStart, &startResult); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	childID := session.SessionID(startResult.SubagentID)
	if childID == "" {
		t.Fatal("continuable start returned an empty child id")
	}
	waitContext, cancelWait := context.WithTimeout(
		context.Background(),
		3*time.Second,
	)
	defer cancelWait()
	waitForContinuableSettlement(t, state, parentHandle, waitContext)
	requests := backend.snapshots()
	if len(requests) != 3 {
		t.Fatalf("report model request count = %d, want 3", len(requests))
	}
	if !hasToolSchema(requests[0].Tools, "report") {
		t.Fatalf("continuable child tools = %#v", requests[0].Tools)
	}
	if !hasUserContent(requests[2].Messages, "selected report content") {
		t.Fatal("parent model request did not contain the child report")
	}
	messages, deriveErr := parentHandle.Subject.SessionValue().DeriveMessages()
	if deriveErr != nil {
		t.Fatal(deriveErr)
	}
	if !hasReportSource(messages, "selected report content", childID) {
		t.Fatal("parent Session did not retain ReportSource attribution")
	}
	if failures := state.eventFailures.snapshot(); len(failures) != 0 {
		t.Fatalf("event observer failures = %#v", failures)
	}
}

func TestReportExtensionReleasesResidentInstallationDuringRuntimeShutdown(
	t *testing.T,
) {
	reportPlugin, reportErr := report.New(subagent.ReportQuiet)
	if reportErr != nil {
		t.Fatal(reportErr)
	}
	requestGate := make(chan struct{})
	backend := &integrationAdapter{
		responses: [][]llm.StreamChunk{
			continuableTextResponse("unreachable while the request is gated"),
		},
		gates: []<-chan struct{}{
			requestGate,
		},
		requestsChanged: make(chan struct{}, 1),
	}
	state, _ := newContinuableIntegrationFixtureWithAdapter(
		t,
		backend,
		reportPlugin,
	)
	parentHandle, createErr := state.agents.Create(
		context.Background(),
		agent.CreateOptions{
			SessionID:    "report-shutdown-parent",
			AgentOptions: state.parentOptions,
		},
	)
	if createErr != nil {
		t.Fatal(createErr)
	}
	started := state.toolRuntime.Execute(
		context.Background(),
		tools.ToolExecutionInput{
			CallID:     "start-resident-report-child",
			RootCallID: "start-resident-report-child",
			Name:       subagenttool.DefaultToolName,
			Arguments: json.RawMessage(`{
  "description": "resident report child",
  "prompt": "Remain active until runtime shutdown."
}`),
			Subject: parentHandle.Subject,
		},
	)
	if started.Failed() {
		failure, _ := started.FailureDetail()
		t.Fatalf("continuable start failed: %#v", failure)
	}
	waitContext, cancelWait := context.WithTimeout(
		context.Background(),
		3*time.Second,
	)
	defer cancelWait()
	if requestErr := backend.waitForRequests(waitContext, 1); requestErr != nil {
		t.Fatal(requestErr)
	}
	requests := backend.snapshots()
	if len(requests) != 1 || !hasToolSchema(requests[0].Tools, "report") {
		t.Fatalf("resident child tools = %#v", requests)
	}
	// The fixture shutdown owns both live Agent Handles. Leaving the request
	// gated proves child-scoped report effects release through structural Agent
	// teardown without a nested topology mutation from Subagent.Dispose.
}

func hasToolSchema(schemas []llm.ToolSchema, selectedName string) bool {
	for _, schema := range schemas {
		if schema.Name == selectedName {
			return true
		}
	}
	return false
}

func hasUserContent(messages []llm.Message, selectedText string) bool {
	for _, messageValue := range messages {
		if messageValue.ConversationRole() != llm.RoleUser {
			continue
		}
		for _, block := range messageValue.ContentValue() {
			plain, matches := block.(llm.PlainTextContent)
			if !matches {
				continue
			}
			textValue, visible := plain.PlainText()
			if visible && textValue == selectedText {
				return true
			}
		}
	}
	return false
}

func hasReportSource(
	messages []llm.Message,
	selectedText string,
	senderID session.SessionID,
) bool {
	for _, messageValue := range messages {
		origin := messageValue.SourceValue()
		if origin == nil || origin.SourceKind() != "subagent-report" {
			continue
		}
		rawOrigin, encodeErr := json.Marshal(origin)
		if encodeErr != nil {
			continue
		}
		var source struct {
			Kind            string            `json:"kind"`
			Form            llm.ContextForm   `json:"form"`
			SenderSessionID session.SessionID `json:"senderSessionId"`
		}
		if decodeErr := json.Unmarshal(rawOrigin, &source); decodeErr != nil ||
			source.Kind != "subagent-report" ||
			source.Form != llm.ContextRelay ||
			source.SenderSessionID != senderID {
			continue
		}
		if hasUserContent([]llm.Message{messageValue}, selectedText) {
			return true
		}
	}
	return false
}
