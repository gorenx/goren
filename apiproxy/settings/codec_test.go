package settingsapi_test

import (
	"encoding/json"
	"fmt"
	"testing"

	api "github.com/gorenx/goren/apiproxy/settings"
	"github.com/gorenx/goren/connection"
)

func codecIssues[T any](decode func(json.RawMessage) (T, []connection.ValidationIssue)) func(string) []connection.ValidationIssue {
	return func(payload string) []connection.ValidationIssue {
		_, issues := decode(json.RawMessage(payload))
		return issues
	}
}

func TestSettingsUpdateCodecsRejectAmbiguousInputs(t *testing.T) {
	for _, entry := range []struct {
		name    string
		decode  func(string) []connection.ValidationIssue
		valid   string
		invalid []string
	}{
		{
			name:   "provider",
			decode: codecIssues(api.DecodeUpdateProviderRequest),
			valid:  `{"apiKeyEnv":"REF","retryPolicy":{"maxRetries":0}}`,
			invalid: []string{
				`{"apiKey":"secret-marker"}`,
				`{"APIKeyEnv":"REF"}`,
				`{"apiKeyEnv":null}`,
				`{"retryPolicy":{"mode":"normal","mode":"always"}}`,
				`{"retryPolicy":{"backoff":{"MaxDelayMs":1}}}`,
				`{"retryPolicy":{"maxRetries":"1"}}`,
			},
		},
		{
			name:   "prompt",
			decode: codecIssues(api.DecodeUpdatePromptRequest),
			valid:  `{"persona":"","includeHarnessIdentity":false}`,
			invalid: []string{
				`{"persona":null}`,
				`{"persona":"a","persona":"b"}`,
				`{"Persona":"a"}`,
				`{"includeHarnessIdentity":"false"}`,
				`{"baseURL":"https://example.test"}`,
			},
		},
		{
			name:   "tool",
			decode: codecIssues(api.DecodeUpdateToolRequest),
			valid:  `{"enabled":false,"visible":true}`,
			invalid: []string{
				`{"enabled":null}`,
				`{"visible":"false"}`,
				`{"Enabled":true}`,
				`{"enabled":true,"enabled":false}`,
				`{"guard":"secret-marker"}`,
				`{"schema":{}}`,
			},
		},
		{
			name:   "mcp",
			decode: codecIssues(api.DecodeUpdateMCPRequest),
			valid:  `{"enabled":true,"connection":{"serverName":"testa","transport":"streamable-http","url":"https://example.test"}}`,
			invalid: []string{
				`{}`,
				`{"servers":[]}`,
				`{"enabled":true,"connection":null}`,
			},
		},
	} {
		t.Run(entry.name, func(t *testing.T) {
			request := func(revision string, value string) string {
				return fmt.Sprintf(`{"name":"testa","expectedRevision":%s,"config":%s}`, revision, value)
			}
			for _, revision := range []string{"0", "9007199254740991"} {
				if issues := entry.decode(request(revision, entry.valid)); len(issues) != 0 {
					t.Fatal(issues)
				}
			}
			for _, revision := range []string{"null", "-1", "1.0", "9007199254740992", `"1"`} {
				if issues := entry.decode(request(revision, entry.valid)); len(issues) == 0 {
					t.Fatal("invalid revision accepted")
				}
			}
			for _, value := range append(entry.invalid, "null", "[]") {
				if issues := entry.decode(request("0", value)); len(issues) == 0 {
					t.Fatalf("invalid group accepted: %s", value)
				}
			}
			for _, payload := range []string{
				`null`, `[]`, `{}`, `{} {}`,
				`{"expectedRevision":0,"config":` + entry.valid + `}`,
				`{"name":"testa","expectedRevision":0,"overrides":` + entry.valid + `}`,
				`{"name":"testa","expectedRevision":0,"config":` + entry.valid + `,"fields":[]}`,
				`{"name":"testa","expectedRevision":0,"expectedRevision":1,"config":` + entry.valid + `}`,
				`{"Name":"testa","expectedRevision":0,"config":` + entry.valid + `}`,
			} {
				if issues := entry.decode(payload); len(issues) == 0 {
					t.Fatalf("invalid envelope accepted: %s", payload)
				}
			}
		})
	}
}

func TestSettingsResetCodecsRequireWholeGroupIdentity(t *testing.T) {
	for _, decode := range []func(string) []connection.ValidationIssue{
		codecIssues(api.DecodeResetProviderRequest),
		codecIssues(api.DecodeResetPromptRequest),
		codecIssues(api.DecodeResetMCPRequest),
		codecIssues(api.DecodeResetToolRequest),
	} {
		if issues := decode(`{"name":"testa","expectedRevision":0}`); len(issues) != 0 {
			t.Fatal(issues)
		}
		for _, payload := range []string{
			`{}`, `null`,
			`{"expectedRevision":0,"fields":["persona"]}`,
			`{"name":"testa","expectedRevision":0,"fields":[]}`,
			`{"name":"testa","expectedRevision":0,"config":{}}`,
			`{"name":"","expectedRevision":0}`,
			`{"name":null,"expectedRevision":0}`,
			`{"name":"testa","expectedRevision":null}`,
			`{"name":"testa","name":"other","expectedRevision":0}`,
		} {
			if issues := decode(payload); len(issues) == 0 {
				t.Fatalf("invalid reset accepted: %s", payload)
			}
		}
	}
}

func TestMCPCodecsRequireCompleteNamedGroups(t *testing.T) {
	valid := `{"name":"testa","expectedRevision":0,"config":{"enabled":false,"connection":{"serverName":"testa","transport":"streamable-http","url":"https://example.test"}}}`
	if _, issues := api.DecodeUpdateMCPRequest(json.RawMessage(valid)); len(issues) != 0 {
		t.Fatal(issues)
	}
	for _, payload := range []string{
		`{"name":"testa","expectedRevision":0,"config":{}}`,
		`{"name":"testa","expectedRevision":0,"config":{"enabled":false}}`,
		`{"name":"testa","expectedRevision":0,"config":{"connection":{"serverName":"testa","transport":"streamable-http","url":"https://example.test"}}}`,
		`{"name":"other","expectedRevision":0,"config":{"enabled":true,"connection":{"serverName":"testa","transport":"streamable-http","url":"https://example.test"}}}`,
		`{"name":"testa","expectedRevision":0,"config":{"enabled":true,"connection":{"serverName":"testa","transport":"streamable-http","url":"https://example.test","URL":"https://wrong.example.test"}}}`,
		`{"name":"testa","expectedRevision":0,"config":{"enabled":true,"connection":{"serverName":"testa","transport":"streamable-http","url":"https://example.test","apiKey":"secret-marker"}}}`,
		`{"name":"testa","expectedRevision":0,"config":{"enabled":true,"enabled":false,"connection":{"serverName":"testa","transport":"streamable-http","url":"https://example.test"}}}`,
		`{"name":"testa","expectedRevision":0,"config":{"enabled":null,"connection":{"serverName":"testa","transport":"streamable-http","url":"https://example.test"}}}`,
		`{"expectedRevision":0,"overrides":{"servers":[]}}`,
	} {
		if _, issues := api.DecodeUpdateMCPRequest(json.RawMessage(payload)); len(issues) == 0 {
			t.Fatalf("invalid complete-group request accepted: %s", payload)
		}
	}
	if _, issues := api.DecodeResetMCPRequest(json.RawMessage(`{"name":"testa","expectedRevision":1}`)); len(issues) != 0 {
		t.Fatal(issues)
	}
	for _, payload := range []string{
		`{"expectedRevision":1,"fields":["servers"]}`,
		`{"name":"testa","expectedRevision":1,"fields":["url"]}`,
		`{"name":"","expectedRevision":1}`,
		`{"name":null,"expectedRevision":1}`,
	} {
		if _, issues := api.DecodeResetMCPRequest(json.RawMessage(payload)); len(issues) == 0 {
			t.Fatalf("invalid group reset accepted: %s", payload)
		}
	}
}
