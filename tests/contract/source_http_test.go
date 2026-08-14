//go:build contract

package contract_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorenx/goren/apiproxy"
	connectionhost "github.com/gorenx/goren/internal/connection"
)

type httpContractCase struct {
	Name        string  `json:"name"`
	Method      string  `json:"method"`
	Path        string  `json:"path"`
	ContentType string  `json:"contentType,omitempty"`
	Body        *string `json:"body,omitempty"`
}

type httpContractObservation struct {
	Name        string `json:"name"`
	Status      int    `json:"status"`
	ContentType string `json:"contentType"`
	Body        string `json:"body"`
}

type canonicalHTTPObservation struct {
	Status    int
	MediaType string
	Body      string
}

func TestPinnedSourceHTTPFailuresMatchGoHost(t *testing.T) {
	repositoryRoot, sourceRoot := contractPaths(t)
	testCases := []httpContractCase{
		{Name: "host describe", Method: http.MethodPost, Path: "/api/host.describe", ContentType: "application/json", Body: textPointer(`{"type":"client-request","rpcId":"ok","method":"host.describe","payload":{}}`)},
		{Name: "media type parameters", Method: http.MethodPost, Path: "/api/host.describe", ContentType: "Application/JSON; charset=utf-8", Body: textPointer(`{"type":"client-request","rpcId":"parameters","method":"host.describe","payload":{}}`)},
		{Name: "unknown fields stripped", Method: http.MethodPost, Path: "/api/host.describe", ContentType: "application/json", Body: textPointer(`{"type":"client-request","rpcId":"extra","method":"host.describe","payload":{"ignored":true},"ignored":true}`)},
		{Name: "empty rpc id", Method: http.MethodPost, Path: "/api/host.describe", ContentType: "application/json", Body: textPointer(`{"type":"client-request","rpcId":"","method":"host.describe","payload":{}}`)},
		{Name: "missing content type", Method: http.MethodPost, Path: "/api/host.describe", Body: textPointer(`{}`)},
		{Name: "wrong content type", Method: http.MethodPost, Path: "/api/host.describe", ContentType: "text/plain", Body: textPointer(`{}`)},
		{Name: "empty JSON body", Method: http.MethodPost, Path: "/api/host.describe", ContentType: "application/json", Body: textPointer("")},
		{Name: "malformed JSON", Method: http.MethodPost, Path: "/api/host.describe", ContentType: "application/json", Body: textPointer(`{oops`)},
		{Name: "null envelope", Method: http.MethodPost, Path: "/api/host.describe", ContentType: "application/json", Body: textPointer(`null`)},
		{Name: "missing payload", Method: http.MethodPost, Path: "/api/host.describe", ContentType: "application/json", Body: textPointer(`{"type":"client-request","rpcId":"missing","method":"host.describe"}`)},
		{Name: "invalid rpc id", Method: http.MethodPost, Path: "/api/host.describe", ContentType: "application/json", Body: textPointer(`{"type":"client-request","rpcId":7,"method":"host.describe","payload":{}}`)},
		{Name: "method mismatch", Method: http.MethodPost, Path: "/api/host.describe", ContentType: "application/json", Body: textPointer(`{"type":"client-request","rpcId":"mismatch","method":"session.list","payload":{}}`)},
		{Name: "invalid method payload", Method: http.MethodPost, Path: "/api/host.describe", ContentType: "application/json", Body: textPointer(`{"type":"client-request","rpcId":"payload","method":"host.describe","payload":null}`)},
		{Name: "unknown method valid JSON", Method: http.MethodPost, Path: "/api/no.such", ContentType: "application/json", Body: textPointer(`{}`)},
		{Name: "unknown method missing content type", Method: http.MethodPost, Path: "/api/no.such", Body: textPointer(`{}`)},
		{Name: "unknown method malformed JSON", Method: http.MethodPost, Path: "/api/no.such", ContentType: "application/json", Body: textPointer(`{oops`)},
		{Name: "wrong HTTP method", Method: http.MethodGet, Path: "/api/host.describe"},
		{Name: "API root", Method: http.MethodPost, Path: "/api"},
		{Name: "respond not pending", Method: http.MethodPost, Path: "/api/respond", ContentType: "application/json", Body: textPointer(`{"type":"client-response","rpcId":"unknown","result":{"ok":true}}`)},
		{Name: "respond bad response", Method: http.MethodPost, Path: "/api/respond", ContentType: "application/json", Body: textPointer(`{"type":"client-request"}`)},
		{Name: "handler failure", Method: http.MethodPost, Path: "/api/host.describe", ContentType: "application/json", Body: textPointer(`{"type":"client-request","rpcId":"crash","method":"host.describe","payload":{}}`)},
	}
	encodedCases, err := json.Marshal(testCases)
	if err != nil {
		t.Fatal(err)
	}
	commandContext, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sourceOutput, err := runTypeScriptInput(commandContext, sourceRoot, encodedCases,
		filepath.Join(repositoryRoot, "tests", "contract", "typescript", "http-reference.ts"), sourceRoot,
	)
	if err != nil {
		t.Fatal(err)
	}
	var sourceObservations []httpContractObservation
	if err := json.Unmarshal(sourceOutput, &sourceObservations); err != nil {
		t.Fatalf("decode source HTTP observations: %v; output = %s", err, sourceOutput)
	}
	goObservations := runGoHTTPContractCases(t, testCases)
	if len(sourceObservations) != len(goObservations) {
		t.Fatalf("observation counts = source %d, Go %d", len(sourceObservations), len(goObservations))
	}
	for index := range sourceObservations {
		sourceObservation := canonicalizeHTTPObservation(t, sourceObservations[index])
		goObservation := canonicalizeHTTPObservation(t, goObservations[index])
		if sourceObservations[index].Name != goObservations[index].Name || sourceObservation != goObservation {
			t.Fatalf("%s differs\nsource = %#v\nGo     = %#v", testCases[index].Name, sourceObservation, goObservation)
		}
	}
}

func runGoHTTPContractCases(t *testing.T, testCases []httpContractCase) []httpContractObservation {
	t.Helper()
	methods := apiproxy.NewCatalog()
	if err := apiproxy.RegisterUnary(methods, apiproxy.HostDescribeMethod, apiproxy.DecodeObject[apiproxy.HostDescribeRequest],
		func(_ context.Context, request apiproxy.Request[apiproxy.HostDescribeRequest]) (apiproxy.Outcome[apiproxy.HostDescription], error) {
			if request.RPCID == "crash" {
				return apiproxy.Outcome[apiproxy.HostDescription]{}, errors.New("dependency failed")
			}
			return apiproxy.OK(apiproxy.HostDescription{
				Version: "0.1.0-rc.5", CWD: "/contract-workspace", AttachedSessions: 0, CanOpenPath: false,
			}), nil
		}); err != nil {
		t.Fatal(err)
	}
	idleMux := func(requestContext context.Context, _ func(apiproxy.StreamRequest[apiproxy.MuxFrame]) error) error {
		<-requestContext.Done()
		return nil
	}
	idleHost := func(requestContext context.Context, _ func(apiproxy.StreamRequest[apiproxy.HostFrame]) error) error {
		<-requestContext.Done()
		return nil
	}
	streams, err := apiproxy.NewEventStreams(idleMux, idleHost)
	if err != nil {
		t.Fatal(err)
	}
	httpHost, err := connectionhost.NewHTTPHost(connectionhost.HTTPConfig{}, methods, streams)
	if err != nil {
		t.Fatal(err)
	}
	testServer := httptest.NewServer(httpHost)
	t.Cleanup(func() {
		testServer.Close()
		closeContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := httpHost.Close(closeContext); err != nil {
			t.Errorf("close Go host: %v", err)
		}
	})

	observations := make([]httpContractObservation, 0, len(testCases))
	for _, testCase := range testCases {
		var requestBody io.Reader
		if testCase.Body != nil {
			requestBody = strings.NewReader(*testCase.Body)
		}
		httpRequest, err := http.NewRequest(testCase.Method, testServer.URL+testCase.Path, requestBody)
		if err != nil {
			t.Fatal(err)
		}
		if testCase.ContentType != "" {
			httpRequest.Header.Set("Content-Type", testCase.ContentType)
		}
		response, err := http.DefaultClient.Do(httpRequest)
		if err != nil {
			t.Fatal(err)
		}
		responseBody, readErr := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		observations = append(observations, httpContractObservation{
			Name: testCase.Name, Status: response.StatusCode,
			ContentType: response.Header.Get("Content-Type"), Body: string(responseBody),
		})
	}
	return observations
}

func canonicalizeHTTPObservation(t *testing.T, observation httpContractObservation) canonicalHTTPObservation {
	t.Helper()
	mediaType := ""
	if observation.ContentType != "" {
		parsedType, _, err := mime.ParseMediaType(observation.ContentType)
		if err != nil {
			t.Fatalf("%s content type %q: %v", observation.Name, observation.ContentType, err)
		}
		mediaType = strings.ToLower(parsedType)
	}
	body := observation.Body
	if observation.Status >= http.StatusInternalServerError && strings.HasPrefix(body, "handler failure:") {
		body = "handler failure"
	} else if json.Valid([]byte(body)) {
		var value any
		if err := json.Unmarshal([]byte(body), &value); err != nil {
			t.Fatal(err)
		}
		if envelope, ok := value.(map[string]any); ok {
			if result, ok := envelope["result"].(map[string]any); ok {
				if resultError, ok := result["error"].(map[string]any); ok {
					if details, ok := resultError["details"].(map[string]any); ok {
						if issues, ok := details["issues"].([]any); ok {
							details["issues"] = len(issues)
						}
					}
				}
			}
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		body = string(encoded)
	}
	return canonicalHTTPObservation{Status: observation.Status, MediaType: mediaType, Body: body}
}

func textPointer(content string) *string {
	return &content
}
