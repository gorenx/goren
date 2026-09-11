package settingsapi_test

import (
	"encoding/json"
	"testing"

	api "github.com/gorenx/goren/apiproxy/settings"
)

func TestCompanionRequestEnforcesPublishedSchema(t *testing.T) {
	for _, content := range []string{
		`{}`,
		`{"enabled":true,"inherit":{"tools":"all","mcp":true},"configuration":"review-config"}`,
		`{"configuration":"independent"}`,
	} {
		if _, issues := api.DecodeUpdateCompanionRequest(json.RawMessage(`{"name":"reviewer","expectedRevision":0,"config":` + content + `}`)); len(issues) != 0 {
			t.Fatalf("valid config rejected: %s: %v", content, issues)
		}
	}
	for _, content := range []string{
		`null`,
		`{"enabled":null}`,
		`{"Enabled":true}`,
		`{"enabled":true,"enabled":false}`,
		`{"inherit":{"tools":"selected"}}`,
		`{"model":{"MaxTokens":1}}`,
		`{"prompt":{"Persona":"hidden"}}`,
		`{"tools":{"read":{"Visible":false}}}`,
		`{"tools":{"report":{}}}`,
		`{"mcp":[{"serverName":"local","transport":"stdio","command":"fixture"}]}`,
		`{"mcp":[{"serverName":"http","transport":"streamable-http","url":"https://example.test","token":"secret-marker"}]}`,
	} {
		if _, issues := api.DecodeUpdateCompanionRequest(json.RawMessage(`{"name":"reviewer","expectedRevision":0,"config":` + content + `}`)); len(issues) == 0 {
			t.Fatalf("invalid config accepted: %s", content)
		}
	}
}

func TestAgentRequestUsesSharedCapabilityValues(t *testing.T) {
	for _, content := range []string{
		`{}`,
		`{"model":{"maxTokens":400},"prompts":{"default":{"persona":"review","includeRuntimeContext":false}},"tools":{"read":{"visible":false}}}`,
		`{"mcp":{"search":{"enabled":true,"connection":{"serverName":"search","transport":"streamable-http","url":"https://example.test/mcp"}}}}`,
	} {
		if _, issues := api.DecodeUpdateAgentRequest(json.RawMessage(`{"name":"review-config","expectedRevision":0,"config":` + content + `}`)); len(issues) != 0 {
			t.Fatalf("valid shared values rejected: %s: %v", content, issues)
		}
	}
	for _, content := range []string{
		`null`, `{"enabled":true}`, `{"inherit":{"tools":"all"}}`, `{"configuration":"other"}`,
		`{"prompt":{"persona":"legacy"}}`, `{"prompts":{"default":{"Persona":"wrong case"}}}`,
		`{"mcp":{"local":{"enabled":true,"connection":{"serverName":"local","transport":"stdio","command":"fixture"}}}}`,
		`{"mcp":{"alias":{"enabled":true,"connection":{"serverName":"other","transport":"streamable-http","url":"https://example.test/mcp"}}}}`,
	} {
		if _, issues := api.DecodeUpdateAgentRequest(json.RawMessage(`{"name":"review-config","expectedRevision":0,"config":` + content + `}`)); len(issues) == 0 {
			t.Fatalf("invalid shared values accepted: %s", content)
		}
	}
}
