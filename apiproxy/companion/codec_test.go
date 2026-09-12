package companionapi

import (
	"encoding/json"
	"testing"

	"github.com/gorenx/goren/connection"
)

func TestManagementRequestsRejectUnknownAndMalformedFields(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		decode  func(json.RawMessage) []connection.ValidationIssue
	}{
		{
			name:    "create requires parent",
			payload: `{"name":"reviewer"}`,
			decode: func(raw json.RawMessage) []connection.ValidationIssue {
				_, issues := DecodeCreateRequest(raw)
				return issues
			},
		},
		{
			name:    "create rejects nested unknown policy",
			payload: `{"parentSessionId":"parent","name":"reviewer","config":{"enabled":true,"inherit":{"Tools":"all"}}}`,
			decode: func(raw json.RawMessage) []connection.ValidationIssue {
				_, issues := DecodeCreateRequest(raw)
				return issues
			},
		},
		{
			name:    "create rejects duplicate parent identity",
			payload: `{"parentSessionId":"parent","parentSessionId":"other","name":"reviewer"}`,
			decode: func(raw json.RawMessage) []connection.ValidationIssue {
				_, issues := DecodeCreateRequest(raw)
				return issues
			},
		},
		{
			name:    "create rejects null policy",
			payload: `{"parentSessionId":"parent","name":"reviewer","config":null}`,
			decode: func(raw json.RawMessage) []connection.ValidationIssue {
				_, issues := DecodeCreateRequest(raw)
				return issues
			},
		},
		{
			name:    "list rejects model-supplied child",
			payload: `{"parentSessionId":"parent","childSessionId":"other"}`,
			decode: func(raw json.RawMessage) []connection.ValidationIssue {
				_, issues := DecodeListRequest(raw)
				return issues
			},
		},
		{
			name:    "configure requires explicit boolean",
			payload: `{"parentSessionId":"parent","name":"reviewer","expectedRevision":1}`,
			decode: func(raw json.RawMessage) []connection.ValidationIssue {
				_, issues := DecodeConfigureRequest(raw)
				return issues
			},
		},
		{
			name:    "remove rejects fractional revision",
			payload: `{"parentSessionId":"parent","name":"reviewer","expectedRevision":1.5}`,
			decode: func(raw json.RawMessage) []connection.ValidationIssue {
				_, issues := DecodeRemoveRequest(raw)
				return issues
			},
		},
		{
			name:    "restart requires observed creation",
			payload: `{"parentSessionId":"parent","name":"reviewer","expectedRevision":1}`,
			decode: func(raw json.RawMessage) []connection.ValidationIssue {
				_, issues := DecodeRestartRequest(raw)
				return issues
			},
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if issues := testCase.decode(json.RawMessage(testCase.payload)); len(issues) == 0 {
				t.Fatal("malformed management request was accepted")
			}
		})
	}
	valid, issues := DecodeCreateRequest(json.RawMessage(`{"parentSessionId":"parent","name":"reviewer","config":{"enabled":true,"inherit":{"tools":"all"}}}`))
	if len(issues) != 0 || valid.Config == nil || !valid.Config.Enabled || valid.ParentSessionID != "parent" {
		t.Fatalf("valid explicit policy was rejected: %+v %+v", valid, issues)
	}
}
