package subagent_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	subagentdelegation "github.com/gorenx/goren/subagent/tools/delegation"
	"github.com/gorenx/goren/subagent/tools/report"
	"github.com/gorenx/goren/tools"
)

func TestReportExtensionDeliversChildSelectedContentToParent(t *testing.T) {
	reportPlugin, reportErr := report.New(report.Quiet)
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
			Name:       subagentdelegation.DefaultToolName,
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
	reportPlugin, reportErr := report.New(report.Quiet)
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
			Name:       subagentdelegation.DefaultToolName,
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
	// Leaving the request gated proves shutdown cancels live child work rather
	// than relying on normal settlement before structural Agent teardown.
	if shutdownErr := state.runtimeEngine.Shutdown(context.Background()); shutdownErr != nil {
		t.Fatalf(
			"Runtime shutdown failed: %v; details: %#v",
			shutdownErr,
			flattenIntegrationErrors(shutdownErr),
		)
	}
	if len(state.agents.List()) != 0 || len(state.sessions.List()) != 0 {
		t.Fatalf(
			"Runtime shutdown retained Agents or Sessions: agents=%d sessions=%d",
			len(state.agents.List()),
			len(state.sessions.List()),
		)
	}
	starts, ends := state.lifecycle.snapshot()
	if len(starts) != 1 || len(ends) != 1 || ends[0].ID != starts[0].ID {
		t.Fatalf("Subagent shutdown lifecycle: starts=%#v ends=%#v", starts, ends)
	}
}

func TestSubagentPluginUnloadRequestsResidentChildClosure(
	t *testing.T,
) {
	reportPlugin, reportErr := report.New(report.Quiet)
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
	parentHandle := state.createParent(t)
	started := state.toolRuntime.Execute(
		context.Background(),
		tools.ToolExecutionInput{
			CallID:     "start-child-before-subagent-unload",
			RootCallID: "start-child-before-subagent-unload",
			Name:       subagentdelegation.DefaultToolName,
			Arguments: json.RawMessage(`{
  "description": "resident child during Subagent unload",
  "prompt": "Remain active until Subagent unload."
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
	waitContext, cancelWait := context.WithTimeout(
		context.Background(),
		3*time.Second,
	)
	defer cancelWait()
	if requestErr := backend.waitForRequests(waitContext, 1); requestErr != nil {
		t.Fatal(requestErr)
	}
	if unloadErr := state.runtimeEngine.Unload(
		context.Background(),
		state.subagentHandle,
	); unloadErr != nil {
		t.Fatalf(
			"Subagent Plugin unload failed: %v; details: %#v",
			unloadErr,
			flattenIntegrationErrors(unloadErr),
		)
	}
	childID := session.SessionID(startResult.SubagentID)
	closePoll := time.NewTicker(time.Millisecond)
	defer closePoll.Stop()
	for {
		_, agentLive := state.agents.Get(childID)
		_, sessionLive := state.sessions.Get(childID)
		if !agentLive && !sessionLive {
			break
		}
		select {
		case <-waitContext.Done():
			t.Fatalf(
				"resident child remained after Subagent Plugin unload: agent=%t session=%t",
				agentLive,
				sessionLive,
			)
		case <-closePoll.C:
		}
	}
	if !state.agents.Contains(parentHandle.Subject) {
		t.Fatal("Subagent Plugin unload closed the ordinary parent Agent")
	}
	if failures := state.observerErrors.snapshot(); len(failures) != 0 {
		t.Fatalf("Subagent Plugin unload reported close failures: %#v", failures)
	}
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
