package connection

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorenx/goren/apiproxy"
	wire "github.com/gorenx/goren/connection"
)

func TestUnaryCarrierContract(t *testing.T) {
	t.Parallel()
	carrier := configuredHost(t)
	tests := []struct {
		name        string
		method      string
		path        string
		contentType string
		body        string
		wantStatus  int
		wantText    string
		wantRPCID   wire.RPCID
		wantCode    wire.RPCErrorCode
	}{
		{
			name: "success", method: http.MethodPost, path: "/api/host.describe", contentType: "application/json; charset=utf-8",
			body: `{"type":"client-request","rpcId":"r-1","method":"host.describe","payload":{}}`, wantStatus: http.StatusOK, wantRPCID: "r-1",
		},
		{name: "unknown path", method: http.MethodPost, path: "/api/no.such", wantStatus: http.StatusNotFound, wantText: "not found"},
		{name: "encoded endpoint", method: http.MethodPost, path: "/api/host%2Edescribe", wantStatus: http.StatusNotFound, wantText: "not found"},
		{name: "wrong method", method: http.MethodGet, path: "/api/host.describe", wantStatus: http.StatusNotFound, wantText: "not found"},
		{
			name: "wrong media type", method: http.MethodPost, path: "/api/host.describe", contentType: "text/plain", body: `{}`,
			wantStatus: http.StatusUnsupportedMediaType, wantText: "content type must be application/json",
		},
		{
			name: "bad json", method: http.MethodPost, path: "/api/host.describe", contentType: "application/json", body: `{oops`,
			wantStatus: http.StatusBadRequest, wantText: "body is not JSON",
		},
		{
			name: "bad envelope sentinel", method: http.MethodPost, path: "/api/host.describe", contentType: "application/json", body: `{"nope":true}`,
			wantStatus: http.StatusOK, wantRPCID: wire.InvalidRequestRPCID, wantCode: wire.ErrorBadRequest,
		},
		{
			name: "bad envelope salvages id", method: http.MethodPost, path: "/api/host.describe", contentType: "application/json", body: `{"rpcId":"salvage"}`,
			wantStatus: http.StatusOK, wantRPCID: "salvage", wantCode: wire.ErrorBadRequest,
		},
		{
			name: "path mismatch", method: http.MethodPost, path: "/api/host.describe", contentType: "application/json",
			body:       `{"type":"client-request","rpcId":"r-2","method":"session.list","payload":{}}`,
			wantStatus: http.StatusOK, wantRPCID: "r-2", wantCode: wire.ErrorBadRequest,
		},
		{
			name: "invalid method payload", method: http.MethodPost, path: "/api/host.describe", contentType: "application/json",
			body:       `{"type":"client-request","rpcId":"r-3","method":"host.describe","payload":null}`,
			wantStatus: http.StatusOK, wantRPCID: "r-3", wantCode: wire.ErrorBadRequest,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			httpRequest := httptest.NewRequest(testCase.method, "http://localhost"+testCase.path, strings.NewReader(testCase.body))
			if testCase.contentType != "" {
				httpRequest.Header.Set("Content-Type", testCase.contentType)
			}
			recorder := httptest.NewRecorder()
			carrier.ServeHTTP(recorder, httpRequest)
			if recorder.Code != testCase.wantStatus {
				t.Fatalf("status = %d, body = %q", recorder.Code, recorder.Body.String())
			}
			if testCase.wantText != "" {
				if recorder.Body.String() != testCase.wantText {
					t.Fatalf("body = %q, want %q", recorder.Body.String(), testCase.wantText)
				}
				return
			}
			var message wire.ServerResponse
			if err := json.Unmarshal(recorder.Body.Bytes(), &message); err != nil {
				t.Fatal(err)
			}
			if message.RPCID != testCase.wantRPCID {
				t.Fatalf("rpcId = %q, want %q", message.RPCID, testCase.wantRPCID)
			}
			if testCase.wantCode != "" && (message.Result.Error == nil || message.Result.Error.Code != testCase.wantCode) {
				t.Fatalf("result = %#v", message.Result)
			}
		})
	}
}

func TestRespondCarrierContract(t *testing.T) {
	t.Parallel()
	carrier := configuredHost(t)
	tests := []struct {
		name       string
		body       string
		wantReason wire.RPCReceiptReason
	}{
		{
			name:       "well formed but not pending",
			body:       `{"type":"client-response","rpcId":"unknown","result":{"ok":true,"value":null}}`,
			wantReason: wire.ReceiptNotPending,
		},
		{name: "malformed response", body: `{"type":"client-request"}`, wantReason: wire.ReceiptBadResponse},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			httpRequest := httptest.NewRequest(http.MethodPost, "http://localhost/api/respond", strings.NewReader(testCase.body))
			httpRequest.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			carrier.ServeHTTP(recorder, httpRequest)
			var receipt wire.RPCReceipt
			if err := json.Unmarshal(recorder.Body.Bytes(), &receipt); err != nil {
				t.Fatal(err)
			}
			if recorder.Code != http.StatusOK || receipt.Accepted || receipt.Reason != testCase.wantReason {
				t.Fatalf("status = %d, receipt = %#v", recorder.Code, receipt)
			}
		})
	}
}

func TestUnaryRequestCancellationReachesProvider(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	methods := apiproxy.NewCatalog()
	operation := func(requestContext context.Context, _ apiproxy.Request[struct{}]) (apiproxy.Outcome[struct{}], error) {
		close(started)
		<-requestContext.Done()
		return apiproxy.Fail[struct{}](wire.RPCError{
			Code: wire.ErrorCancelled, Message: "cancelled", Details: json.RawMessage(`{}`),
		}), nil
	}
	if err := apiproxy.RegisterUnary(methods, "test.wait", apiproxy.DecodeObject[struct{}], operation); err != nil {
		t.Fatal(err)
	}
	carrier, err := NewHTTPHost(HTTPConfig{}, methods)
	if err != nil {
		t.Fatal(err)
	}
	requestContext, cancel := context.WithCancel(context.Background())
	httpRequest := httptest.NewRequest(http.MethodPost, "http://localhost/api/test.wait", strings.NewReader(
		`{"type":"client-request","rpcId":"r-wait","method":"test.wait","payload":{}}`,
	)).WithContext(requestContext)
	httpRequest.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		carrier.ServeHTTP(recorder, httpRequest)
	}()
	<-started
	cancel()
	<-done
	var message wire.ServerResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &message); err != nil {
		t.Fatal(err)
	}
	if message.Result.Error == nil || message.Result.Error.Code != wire.ErrorCancelled {
		t.Fatalf("result = %#v", message.Result)
	}
}

func TestBodyLimit(t *testing.T) {
	t.Parallel()
	methods := apiproxy.NewCatalog()
	source := apiproxy.HostDescriptionFunc(func(context.Context) (apiproxy.HostDescription, error) {
		return apiproxy.HostDescription{}, nil
	})
	if err := apiproxy.RegisterHostDescribe(methods, source); err != nil {
		t.Fatal(err)
	}
	carrier, err := NewHTTPHost(HTTPConfig{MaxBodyBytes: 8}, methods)
	if err != nil {
		t.Fatal(err)
	}
	httpRequest := httptest.NewRequest(http.MethodPost, "http://localhost/api/host.describe", strings.NewReader(`{"more":"than eight"}`))
	httpRequest.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	carrier.ServeHTTP(recorder, httpRequest)
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, body = %q", recorder.Code, recorder.Body.String())
	}
}

func TestTechnicalProviderFailureIsHTTP500(t *testing.T) {
	t.Parallel()
	methods := apiproxy.NewCatalog()
	source := apiproxy.HostDescriptionFunc(func(context.Context) (apiproxy.HostDescription, error) {
		return apiproxy.HostDescription{}, errors.New("impl crashed")
	})
	if err := apiproxy.RegisterHostDescribe(methods, source); err != nil {
		t.Fatal(err)
	}
	carrier, err := NewHTTPHost(HTTPConfig{}, methods)
	if err != nil {
		t.Fatal(err)
	}
	httpRequest := httptest.NewRequest(http.MethodPost, "http://localhost/api/host.describe", strings.NewReader(
		`{"type":"client-request","rpcId":"r-1","method":"host.describe","payload":{}}`,
	))
	httpRequest.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	carrier.ServeHTTP(recorder, httpRequest)
	if recorder.Code != http.StatusInternalServerError || !strings.Contains(recorder.Body.String(), "impl crashed") {
		t.Fatalf("status = %d, body = %q", recorder.Code, recorder.Body.String())
	}
}

func configuredHost(t *testing.T) *HTTPHost {
	t.Helper()
	methods := apiproxy.NewCatalog()
	source := apiproxy.HostDescriptionFunc(func(context.Context) (apiproxy.HostDescription, error) {
		return apiproxy.HostDescription{
			Version: "0.1.0-rc.5", CWD: "/workspace", AttachedSessions: 0, CanOpenPath: false,
		}, nil
	})
	if err := apiproxy.RegisterHostDescribe(methods, source); err != nil {
		t.Fatal(err)
	}
	carrier, err := NewHTTPHost(HTTPConfig{}, methods)
	if err != nil {
		t.Fatal(err)
	}
	return carrier
}
