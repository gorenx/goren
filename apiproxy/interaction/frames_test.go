package interaction

import (
	"testing"

	"github.com/gorenx/goren/apiproxy/stream"
)

func TestInteractionFramesPreserveCanonicalJSON(t *testing.T) {
	for _, example := range []struct {
		payload  stream.MuxFrame
		expected string
	}{
		{ApprovalRequestedFrame{
			SessionID:  "s",
			ApprovalID: "a",
			ToolName:   "bash",
		}, `{"type":"approval/requested","sessionId":"s","approvalId":"a","toolName":"bash"}`},
		{ApprovalResolvedFrame{
			SessionID:  "s",
			ApprovalID: "a",
			Outcome:    ApprovalAllowedOnce,
		}, `{"type":"approval/resolved","sessionId":"s","approvalId":"a","outcome":"allowed-once"}`},
		{QuestionRequestedFrame{
			SessionID: "s",
			Questions: []AskUserQuestionItem{{
				ID:       "q",
				Question: "Continue?",
			}},
		}, `{"type":"question/requested","sessionId":"s","questions":[{"id":"q","question":"Continue?"}]}`},
		{QuestionResolvedFrame{
			SessionID:     "s",
			QuestionRPCID: "r",
			Outcome:       QuestionCancelled,
		}, `{"type":"question/resolved","sessionId":"s","questionRpcId":"r","outcome":"cancelled"}`},
	} {
		encoded, err := stream.EncodeMuxFrame(example.payload)
		if err != nil || string(encoded) != example.expected {
			t.Fatalf("%T: %s %v", example.payload, encoded, err)
		}
	}
	for _, invalid := range []stream.MuxFrame{ApprovalRequestedFrame{}, ApprovalResolvedFrame{
		SessionID:  "s",
		ApprovalID: "a",
		Outcome:    "invalid",
	}, QuestionRequestedFrame{
		SessionID: "s",
	}, QuestionResolvedFrame{
		SessionID: "s",
		Outcome:   "invalid",
	}} {
		if _, err := stream.EncodeMuxFrame(invalid); err == nil {
			t.Fatalf("accepted %T", invalid)
		}
	}
}
