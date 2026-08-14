package apiproxy_test

import (
	"encoding/json"
	"testing"

	"github.com/gorenx/goren/apiproxy"
	"github.com/gorenx/goren/connection"
)

func TestEncodeEveryMuxFrameBranch(t *testing.T) {
	t.Parallel()
	emptyItems := []apiproxy.QueuedInboxItem{}
	emptyJobs := []apiproxy.JobView{}
	questions := []apiproxy.AskUserQuestionItem{{ID: "question-1", Question: "Continue?"}}
	tests := []struct {
		name     string
		payload  apiproxy.MuxFrame
		wantType string
	}{
		{name: "session event", payload: apiproxy.SessionEventFrame{
			SessionID: "session-1", Event: apiproxy.SessionEvent{Type: "turn/start", Seq: 0, Time: 5, Data: json.RawMessage(`{"turn":1}`)},
		}, wantType: "session/event"},
		{name: "session subscribed", payload: apiproxy.SessionSubscribedFrame{SessionID: "session-1", LastSeq: -1}, wantType: "session/subscribed"},
		{name: "approval requested", payload: apiproxy.ApprovalRequestedFrame{SessionID: "session-1", ApprovalID: "approval-1", ToolName: "bash"}, wantType: "approval/requested"},
		{name: "approval resolved", payload: apiproxy.ApprovalResolvedFrame{SessionID: "session-1", ApprovalID: "approval-1", Outcome: apiproxy.ApprovalAllowedOnce}, wantType: "approval/resolved"},
		{name: "question requested", payload: apiproxy.QuestionRequestedFrame{SessionID: "session-1", Questions: questions}, wantType: "question/requested"},
		{name: "question resolved", payload: apiproxy.QuestionResolvedFrame{SessionID: "session-1", QuestionRPCID: "question-rpc", Outcome: apiproxy.QuestionAnswered}, wantType: "question/resolved"},
		{name: "session queue", payload: apiproxy.SessionQueueFrame{SessionID: "session-1", Items: emptyItems}, wantType: "session/queue"},
		{name: "session jobs", payload: apiproxy.SessionJobsFrame{SessionID: "session-1", Jobs: emptyJobs}, wantType: "session/jobs"},
		{name: "session projection", payload: apiproxy.SessionProjectionFrame{SessionID: "session-1", Key: "todos", Value: json.RawMessage(`null`), Seq: 0}, wantType: "session/projection"},
		{name: "stream error", payload: apiproxy.StreamErrorFrame{Error: connection.RPCError{Code: connection.ErrorInternal, Message: "boom", Details: json.RawMessage(`{}`)}}, wantType: "stream/error"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			encoded, err := apiproxy.EncodeMuxFrame(testCase.payload)
			if err != nil {
				t.Fatal(err)
			}
			assertFrameType(t, encoded, testCase.wantType)
		})
	}
}

func TestEncodeEveryHostFrameBranch(t *testing.T) {
	t.Parallel()
	emptySessions := []apiproxy.SessionID{}
	emptyWorkspaces := []apiproxy.WorkspaceID{}
	emptyArguments := []json.RawMessage{}
	tests := []struct {
		name     string
		payload  apiproxy.HostFrame
		wantType string
	}{
		{name: "session added", payload: apiproxy.HostSessionAddedFrame{SessionID: "session-1", Blank: true}, wantType: "host/session-added"},
		{name: "session removed", payload: apiproxy.HostSessionRemovedFrame{SessionID: "session-1"}, wantType: "host/session-removed"},
		{name: "session status", payload: apiproxy.HostSessionStatusFrame{SessionID: "session-1", Running: true}, wantType: "host/session-status"},
		{name: "agent error", payload: apiproxy.HostAgentErrorFrame{SessionID: "session-1", Message: "boom"}, wantType: "host/agent-error"},
		{name: "workspace changed", payload: apiproxy.HostWorkspaceChangedFrame{Workspace: apiproxy.WorkspaceView{
			WorkspaceID: "workspace-1", Path: "/workspace", Title: "Workspace", SessionIDs: emptySessions, CreatedAt: "0", UpdatedAt: "0",
		}}, wantType: "host/workspace-changed"},
		{name: "workspace removed", payload: apiproxy.HostWorkspaceRemovedFrame{WorkspaceID: "workspace-1"}, wantType: "host/workspace-removed"},
		{name: "workspace order", payload: apiproxy.HostWorkspaceOrderChangedFrame{WorkspaceIDs: emptyWorkspaces}, wantType: "host/workspace-order-changed"},
		{name: "archive set", payload: apiproxy.HostArchivedSessionsChangedFrame{ArchivedSessionIDs: emptySessions}, wantType: "host/archived-sessions-changed"},
		{name: "remote event", payload: apiproxy.HostRemoteEventFrame{Event: "commands/change", Args: emptyArguments}, wantType: "host/remote-event"},
		{name: "stream error", payload: apiproxy.StreamErrorFrame{Error: connection.RPCError{Code: connection.ErrorInternal, Message: "boom", Details: json.RawMessage(`{}`)}}, wantType: "stream/error"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			encoded, err := apiproxy.EncodeHostFrame(testCase.payload)
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
		payload apiproxy.MuxFrame
	}{
		{name: "empty session", payload: apiproxy.SessionSubscribedFrame{}},
		{name: "negative event seq", payload: apiproxy.SessionEventFrame{SessionID: "s", Event: apiproxy.SessionEvent{Seq: -1}}},
		{name: "empty approval id", payload: apiproxy.ApprovalRequestedFrame{SessionID: "s"}},
		{name: "unknown approval outcome", payload: apiproxy.ApprovalResolvedFrame{SessionID: "s", ApprovalID: "a", Outcome: "maybe"}},
		{name: "empty question batch", payload: apiproxy.QuestionRequestedFrame{SessionID: "s", Questions: []apiproxy.AskUserQuestionItem{}}},
		{name: "unknown question intent", payload: apiproxy.QuestionRequestedFrame{SessionID: "s", Questions: []apiproxy.AskUserQuestionItem{{Intent: &apiproxy.QuestionIntent{Kind: "poll"}}}}},
		{name: "unknown queue placement", payload: apiproxy.SessionQueueFrame{SessionID: "s", Items: []apiproxy.QueuedInboxItem{{ID: "m", Placement: "later"}}}},
		{name: "nil queue", payload: apiproxy.SessionQueueFrame{SessionID: "s"}},
		{name: "invalid job", payload: apiproxy.SessionJobsFrame{SessionID: "s", Jobs: []apiproxy.JobView{{ID: "job-1", Kind: "bash", Label: "run", Status: "pending"}}}},
		{name: "empty projection key", payload: apiproxy.SessionProjectionFrame{SessionID: "s", Value: json.RawMessage(`null`)}},
		{name: "negative projection seq", payload: apiproxy.SessionProjectionFrame{SessionID: "s", Key: "todos", Seq: -1}},
		{name: "unknown stream error", payload: apiproxy.StreamErrorFrame{Error: connection.RPCError{Code: "unknown", Details: json.RawMessage(`{}`)}}},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if _, err := apiproxy.EncodeMuxFrame(testCase.payload); err == nil {
				t.Fatal("invalid mux frame was encoded")
			}
		})
	}
}

func TestHostFrameValidationMatchesClosedContract(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		payload apiproxy.HostFrame
	}{
		{name: "unknown origin", payload: apiproxy.HostSessionAddedFrame{SessionID: "s", Origin: "user"}},
		{name: "empty workspace", payload: apiproxy.HostWorkspaceRemovedFrame{}},
		{name: "nil workspace order", payload: apiproxy.HostWorkspaceOrderChangedFrame{}},
		{name: "empty archived session", payload: apiproxy.HostArchivedSessionsChangedFrame{ArchivedSessionIDs: []apiproxy.SessionID{""}}},
		{name: "empty remote event", payload: apiproxy.HostRemoteEventFrame{Args: []json.RawMessage{}}},
		{name: "nil remote args", payload: apiproxy.HostRemoteEventFrame{Event: "commands/change"}},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if _, err := apiproxy.EncodeHostFrame(testCase.payload); err == nil {
				t.Fatal("invalid host frame was encoded")
			}
		})
	}
}

func TestFrameEncodingPreservesWideOwnerFields(t *testing.T) {
	t.Parallel()
	falseValue := false
	emptyOptions := []apiproxy.QuestionOption{}
	payload := apiproxy.QuestionRequestedFrame{
		SessionID: "session-1",
		Questions: []apiproxy.AskUserQuestionItem{{
			ID: "question-1", Question: "Continue?", Options: &emptyOptions, MultiSelect: &falseValue,
		}},
	}
	encoded, err := apiproxy.EncodeMuxFrame(payload)
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
