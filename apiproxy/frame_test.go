package apiproxy_test
import workspaceapi "github.com/gorenx/goren/apiproxy/workspace"

import sessionframes "github.com/gorenx/goren/apiproxy/session/events"


import (
	"encoding/json"
	"github.com/gorenx/goren/apiproxy/interaction"
	"github.com/gorenx/goren/apiproxy/stream"
	"testing"

	"github.com/gorenx/goren/apiproxy"
	"github.com/gorenx/goren/connection"
)

func TestEncodeEveryMuxFrameBranch(t *testing.T) {
	t.Parallel()
	emptyItems := []sessionframes.QueueItem{}
	emptyJobs := []sessionframes.Job{}
	questions := []interaction.AskUserQuestionItem{{ID: "question-1", Question: "Continue?"}}
	tests := []struct {
		name     string
		payload  stream.MuxFrame
		wantType string
	}{
		{name: "session event", payload: sessionframes.EventFrame{
			SessionID: "session-1", Event: sessionframes.LogEvent{Type: "turn/start", Seq: 0, Time: 5, Data: json.RawMessage(`{"turn":1}`)},
		}, wantType: "session/event"},
		{name: "session subscribed", payload: sessionframes.SubscribedFrame{SessionID: "session-1", LastSeq: -1}, wantType: "session/subscribed"},
		{name: "approval requested", payload: interaction.ApprovalRequestedFrame{SessionID: "session-1", ApprovalID: "approval-1", ToolName: "bash"}, wantType: "approval/requested"},
		{name: "approval resolved", payload: interaction.ApprovalResolvedFrame{SessionID: "session-1", ApprovalID: "approval-1", Outcome: interaction.ApprovalAllowedOnce}, wantType: "approval/resolved"},
		{name: "question requested", payload: interaction.QuestionRequestedFrame{SessionID: "session-1", Questions: questions}, wantType: "question/requested"},
		{name: "question resolved", payload: interaction.QuestionResolvedFrame{SessionID: "session-1", QuestionRPCID: "question-rpc", Outcome: interaction.QuestionAnswered}, wantType: "question/resolved"},
		{name: "session queue", payload: sessionframes.QueueFrame{SessionID: "session-1", Items: emptyItems}, wantType: "session/queue"},
		{name: "session jobs", payload: sessionframes.JobsFrame{SessionID: "session-1", Jobs: emptyJobs}, wantType: "session/jobs"},
		{name: "session projection", payload: sessionframes.ProjectionFrame{SessionID: "session-1", Key: "todos", Value: json.RawMessage(`null`), Seq: 0}, wantType: "session/projection"},
		{name: "stream error", payload: stream.ErrorFrame{Error: connection.RPCError{Code: connection.ErrorInternal, Message: "boom", Details: json.RawMessage(`{}`)}}, wantType: "stream/error"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			encoded, err := stream.EncodeMuxFrame(testCase.payload)
			if err != nil {
				t.Fatal(err)
			}
			assertFrameType(t, encoded, testCase.wantType)
		})
	}
}

func TestEncodeEveryHostFrameBranch(t *testing.T) {
	t.Parallel()
	emptySessions := []sessionframes.SessionID{}
	emptyWorkspaces := []workspaceapi.ID{}
	emptyArguments := []json.RawMessage{}
	tests := []struct {
		name     string
		payload  stream.HostFrame
		wantType string
	}{
		{name: "session added", payload: sessionframes.AddedFrame{SessionID: "session-1", Blank: true}, wantType: "host/session-added"},
		{name: "session removed", payload: sessionframes.RemovedFrame{SessionID: "session-1"}, wantType: "host/session-removed"},
		{name: "session status", payload: sessionframes.StatusFrame{SessionID: "session-1", Running: true}, wantType: "host/session-status"},
		{name: "agent error", payload: sessionframes.AgentErrorFrame{SessionID: "session-1", Message: "boom"}, wantType: "host/agent-error"},
		{name: "workspace changed", payload: workspaceapi.ChangedFrame{Workspace: workspaceapi.View{
			WorkspaceID: "workspace-1", Path: "/workspace", Title: "Workspace", SessionIDs: emptySessions, CreatedAt: "0", UpdatedAt: "0",
		}}, wantType: "host/workspace-changed"},
		{name: "workspace removed", payload: workspaceapi.RemovedFrame{WorkspaceID: "workspace-1"}, wantType: "host/workspace-removed"},
		{name: "workspace order", payload: workspaceapi.OrderChangedFrame{WorkspaceIDs: emptyWorkspaces}, wantType: "host/workspace-order-changed"},
		{name: "archive set", payload: workspaceapi.ArchivedSessionsChangedFrame{ArchivedSessionIDs: emptySessions}, wantType: "host/archived-sessions-changed"},
		{name: "remote event", payload: apiproxy.HostRemoteEventFrame{Event: "commands/change", Args: emptyArguments}, wantType: "host/remote-event"},
		{name: "stream error", payload: stream.ErrorFrame{Error: connection.RPCError{Code: connection.ErrorInternal, Message: "boom", Details: json.RawMessage(`{}`)}}, wantType: "stream/error"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			encoded, err := stream.EncodeHostFrame(testCase.payload)
			if err != nil {
				t.Fatal(err)
			}
			assertFrameType(t, encoded, testCase.wantType)
		})
	}
}

func TestMuxFrameValidationMatchesClosedContract(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		payload stream.MuxFrame
	}{
		{name: "empty session", payload: sessionframes.SubscribedFrame{}},
		{name: "negative event seq", payload: sessionframes.EventFrame{SessionID: "s", Event: sessionframes.LogEvent{Seq: -1}}},
		{name: "empty approval id", payload: interaction.ApprovalRequestedFrame{SessionID: "s"}},
		{name: "unknown approval outcome", payload: interaction.ApprovalResolvedFrame{SessionID: "s", ApprovalID: "a", Outcome: "maybe"}},
		{name: "empty question batch", payload: interaction.QuestionRequestedFrame{SessionID: "s", Questions: []interaction.AskUserQuestionItem{}}},
		{name: "unknown question intent", payload: interaction.QuestionRequestedFrame{SessionID: "s", Questions: []interaction.AskUserQuestionItem{{Intent: &interaction.QuestionIntent{Kind: "poll"}}}}},
		{name: "unknown queue placement", payload: sessionframes.QueueFrame{SessionID: "s", Items: []sessionframes.QueueItem{{ID: "m", Placement: "later"}}}},
		{name: "nil queue", payload: sessionframes.QueueFrame{SessionID: "s"}},
		{name: "invalid job", payload: sessionframes.JobsFrame{SessionID: "s", Jobs: []sessionframes.Job{{ID: "job-1", Kind: "bash", Label: "run", Status: "pending"}}}},
		{name: "empty projection key", payload: sessionframes.ProjectionFrame{SessionID: "s", Value: json.RawMessage(`null`)}},
		{name: "negative projection seq", payload: sessionframes.ProjectionFrame{SessionID: "s", Key: "todos", Seq: -1}},
		{name: "unknown stream error", payload: stream.ErrorFrame{Error: connection.RPCError{Code: "unknown", Details: json.RawMessage(`{}`)}}},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if _, err := stream.EncodeMuxFrame(testCase.payload); err == nil {
				t.Fatal("invalid mux frame was encoded")
			}
		})
	}
}

func TestHostFrameValidationMatchesClosedContract(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		payload stream.HostFrame
	}{
		{name: "unknown origin", payload: sessionframes.AddedFrame{SessionID: "s", Origin: "user"}},
		{name: "empty workspace", payload: workspaceapi.RemovedFrame{}},
		{name: "nil workspace order", payload: workspaceapi.OrderChangedFrame{}},
		{name: "empty archived session", payload: workspaceapi.ArchivedSessionsChangedFrame{ArchivedSessionIDs: []sessionframes.SessionID{""}}},
		{name: "empty remote event", payload: apiproxy.HostRemoteEventFrame{Args: []json.RawMessage{}}},
		{name: "nil remote args", payload: apiproxy.HostRemoteEventFrame{Event: "commands/change"}},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if _, err := stream.EncodeHostFrame(testCase.payload); err == nil {
				t.Fatal("invalid host frame was encoded")
			}
		})
	}
}

func TestFrameEncodingPreservesWideOwnerFields(t *testing.T) {
	t.Parallel()
	falseValue := false
	emptyOptions := []interaction.QuestionOption{}
	payload := interaction.QuestionRequestedFrame{
		SessionID: "session-1",
		Questions: []interaction.AskUserQuestionItem{{
			ID: "question-1", Question: "Continue?", Options: &emptyOptions, MultiSelect: &falseValue,
		}},
	}
	encoded, err := stream.EncodeMuxFrame(payload)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"type":"question/requested","sessionId":"session-1","questions":[{"id":"question-1","question":"Continue?","options":[],"multiSelect":false}]}`
	if string(encoded) != want {
		t.Fatalf("encoded = %s\nwant    = %s", encoded, want)
	}
}

func assertFrameType(t *testing.T, encoded json.RawMessage, want string) {
	t.Helper()
	var fields struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if fields.Type != want {
		t.Fatalf("type = %q, want %q; frame = %s", fields.Type, want, encoded)
	}
}
