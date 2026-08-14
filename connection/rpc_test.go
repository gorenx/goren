package connection_test

import (
	"encoding/json"
	"testing"

	"github.com/gorenx/goren/connection"
)

func TestDecodeClientRequest(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		body       string
		wantRPCID  connection.RPCID
		wantMethod string
		wantIssues bool
	}{
		{
			name: "valid envelope", body: `{"type":"client-request","rpcId":"r-1","method":"host.describe","payload":{},"ignored":true}`,
			wantRPCID: "r-1", wantMethod: "host.describe",
		},
		{name: "wrong type", body: `{"type":"server-request","rpcId":"r-1","method":"host.describe","payload":{}}`, wantIssues: true},
		{name: "missing payload", body: `{"type":"client-request","rpcId":"r-1","method":"host.describe"}`, wantIssues: true},
		{name: "non object", body: `[]`, wantIssues: true},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			message, issues := connection.DecodeClientRequest(json.RawMessage(testCase.body))
			if (len(issues) != 0) != testCase.wantIssues {
				t.Fatalf("issues = %#v, wantIssues = %v", issues, testCase.wantIssues)
			}
			if testCase.wantIssues {
				return
			}
			if message.RPCID != testCase.wantRPCID || message.Method != testCase.wantMethod {
				t.Fatalf("message = %#v", message)
			}
		})
	}
}

func TestDecodeClientResponse(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		body       string
		wantIssues bool
	}{
		{name: "success", body: `{"type":"client-response","rpcId":"r-1","result":{"ok":true,"value":null}}`},
		{name: "void success", body: `{"type":"client-response","rpcId":"r-1","result":{"ok":true}}`},
		{name: "known failure", body: `{"type":"client-response","rpcId":"r-1","result":{"ok":false,"error":{"code":"cancelled","message":"cancelled","details":{}}}}`},
		{name: "typed failure details", body: `{"type":"client-response","rpcId":"r-1","result":{"ok":false,"error":{"code":"session-not-found","message":"missing","details":{"sessionId":"s-1"}}}}`},
		{name: "invalid failure details", body: `{"type":"client-response","rpcId":"r-1","result":{"ok":false,"error":{"code":"session-not-found","message":"missing","details":{}}}}`, wantIssues: true},
		{name: "unknown error code", body: `{"type":"client-response","rpcId":"r-1","result":{"ok":false,"error":{"code":"other","message":"failed","details":{}}}}`, wantIssues: true},
		{name: "missing result", body: `{"type":"client-response","rpcId":"r-1"}`, wantIssues: true},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			_, issues := connection.DecodeClientResponse(json.RawMessage(testCase.body))
			if (len(issues) != 0) != testCase.wantIssues {
				t.Fatalf("issues = %#v, wantIssues = %v", issues, testCase.wantIssues)
			}
		})
	}
}

func TestBadRequestUsesArrayForEmptyIssues(t *testing.T) {
	t.Parallel()
	body, err := json.Marshal(connection.Failure(connection.BadRequest("bad", nil)))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != `{"ok":false,"error":{"code":"bad-request","message":"bad","details":{"issues":[]}}}` {
		t.Fatalf("body = %s", body)
	}
}

func TestRPCErrorValidityUsesCodeSpecificDetails(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		rpcError  connection.RPCError
		wantValid bool
	}{
		{
			name:      "valid internal error",
			rpcError:  connection.RPCError{Code: connection.ErrorInternal, Message: "failed", Details: json.RawMessage(`{}`)},
			wantValid: true,
		},
		{
			name:      "valid typed details",
			rpcError:  connection.RPCError{Code: connection.ErrorSessionNotFound, Message: "missing", Details: json.RawMessage(`{"sessionId":"s-1"}`)},
			wantValid: true,
		},
		{
			name:     "missing typed details",
			rpcError: connection.RPCError{Code: connection.ErrorSessionNotFound, Message: "missing", Details: json.RawMessage(`{}`)},
		},
		{
			name:     "unknown code",
			rpcError: connection.RPCError{Code: "unknown", Message: "failed", Details: json.RawMessage(`{}`)},
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := testCase.rpcError.Valid(); got != testCase.wantValid {
				t.Fatalf("Valid() = %v, want %v", got, testCase.wantValid)
			}
		})
	}
}
