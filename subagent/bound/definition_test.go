package bound_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
	boundcontract "github.com/gorenx/goren/subagent/bound"
	"github.com/gorenx/goren/tools"
)

func TestDefinitionRoundTripsStrictly(t *testing.T) {
	t.Parallel()
	maximumTokens := 1024
	maximumDepth := int64(4)
	definitionValue, err := boundcontract.NewDefinition(
		boundcontract.Draft{
			Name:         "researcher",
			Enabled:      true,
			SystemPrompt: "You are the background researcher.",
			AgentOptions: &agent.Options{
				Provider:  "provider",
				Model:     "model",
				MaxTokens: &maximumTokens,
			},
			MaxDepth: &maximumDepth,
			ToolRestriction: &tools.ToolRestriction{
				Allow: []string{"search"},
			},
			Extensions: []string{"memory", "report"},
		},
		3,
	)
	if err != nil {
		t.Fatal(err)
	}
	rawValue, err := json.Marshal(definitionValue)
	if err != nil {
		t.Fatal(err)
	}
	var decoded boundcontract.Definition
	if err = json.Unmarshal(rawValue, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Name != definitionValue.Name ||
		decoded.Revision != definitionValue.Revision ||
		decoded.SystemPrompt != definitionValue.SystemPrompt ||
		decoded.AgentOptions == nil ||
		decoded.AgentOptions.MaxTokens == nil ||
		*decoded.AgentOptions.MaxTokens != maximumTokens ||
		decoded.MaxDepth == nil ||
		*decoded.MaxDepth != maximumDepth ||
		decoded.ToolRestriction == nil ||
		len(decoded.ToolRestriction.Allow) != 1 ||
		len(decoded.Extensions) != 2 {
		t.Fatalf("decoded Definition = %#v", decoded)
	}
	if decoded.ToolRestriction.Allow == nil {
		t.Fatal("Definition lost its allow list")
	}
	if err = json.Unmarshal(
		[]byte(`{"name":"researcher","revision":1,"enabled":true,"systemPrompt":"prompt","extensions":[],"extra":true}`),
		&decoded,
	); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown-field error = %v", err)
	}
	if err = json.Unmarshal(append(rawValue, []byte(`{}`)...), &decoded); err == nil {
		t.Fatal("Definition accepted multiple JSON values")
	}
}

func TestDraftRejectsRevisionEvenWhenNull(t *testing.T) {
	t.Parallel()
	var candidate boundcontract.Draft
	if err := json.Unmarshal(
		[]byte(`{"name":"researcher","revision":null,"enabled":true,"systemPrompt":"prompt","extensions":[]}`),
		&candidate,
	); err == nil {
		t.Fatal("Draft accepted a revision field")
	}
}

func TestDraftJSONRequiresCompleteNonNullFields(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		caseName string
		payload  string
	}{
		{
			caseName: "missing name",
			payload:  `{"enabled":true,"systemPrompt":"prompt","extensions":[]}`,
		},
		{
			caseName: "null name",
			payload:  `{"name":null,"enabled":true,"systemPrompt":"prompt","extensions":[]}`,
		},
		{
			caseName: "missing enabled",
			payload:  `{"name":"researcher","systemPrompt":"prompt","extensions":[]}`,
		},
		{
			caseName: "null enabled",
			payload:  `{"name":"researcher","enabled":null,"systemPrompt":"prompt","extensions":[]}`,
		},
		{
			caseName: "missing prompt",
			payload:  `{"name":"researcher","enabled":true,"extensions":[]}`,
		},
		{
			caseName: "null prompt",
			payload:  `{"name":"researcher","enabled":true,"systemPrompt":null,"extensions":[]}`,
		},
		{
			caseName: "missing extensions",
			payload:  `{"name":"researcher","enabled":true,"systemPrompt":"prompt"}`,
		},
		{
			caseName: "null extensions",
			payload:  `{"name":"researcher","enabled":true,"systemPrompt":"prompt","extensions":null}`,
		},
		{
			caseName: "null agent options",
			payload:  `{"name":"researcher","enabled":true,"systemPrompt":"prompt","agentOptions":null,"extensions":[]}`,
		},
		{
			caseName: "null provider",
			payload:  `{"name":"researcher","enabled":true,"systemPrompt":"prompt","agentOptions":{"provider":null},"extensions":[]}`,
		},
		{
			caseName: "null model",
			payload:  `{"name":"researcher","enabled":true,"systemPrompt":"prompt","agentOptions":{"model":null},"extensions":[]}`,
		},
		{
			caseName: "null maximum tokens",
			payload:  `{"name":"researcher","enabled":true,"systemPrompt":"prompt","agentOptions":{"maxTokens":null},"extensions":[]}`,
		},
		{
			caseName: "unknown agent option",
			payload:  `{"name":"researcher","enabled":true,"systemPrompt":"prompt","agentOptions":{"temperature":1},"extensions":[]}`,
		},
		{
			caseName: "null maximum depth",
			payload:  `{"name":"researcher","enabled":true,"systemPrompt":"prompt","maxDepth":null,"extensions":[]}`,
		},
		{
			caseName: "null tool restriction",
			payload:  `{"name":"researcher","enabled":true,"systemPrompt":"prompt","toolRestriction":null,"extensions":[]}`,
		},
		{
			caseName: "empty tool restriction",
			payload:  `{"name":"researcher","enabled":true,"systemPrompt":"prompt","toolRestriction":{},"extensions":[]}`,
		},
		{
			caseName: "null allow list",
			payload:  `{"name":"researcher","enabled":true,"systemPrompt":"prompt","toolRestriction":{"allow":null},"extensions":[]}`,
		},
		{
			caseName: "null allow item",
			payload:  `{"name":"researcher","enabled":true,"systemPrompt":"prompt","toolRestriction":{"allow":[null]},"extensions":[]}`,
		},
		{
			caseName: "null extension item",
			payload:  `{"name":"researcher","enabled":true,"systemPrompt":"prompt","extensions":[null]}`,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.caseName, func(t *testing.T) {
			t.Parallel()
			var candidate boundcontract.Draft
			if err := json.Unmarshal([]byte(testCase.payload), &candidate); err == nil {
				t.Fatal("invalid Draft JSON was accepted")
			}
		})
	}
}

func TestDraftJSONPreservesFalseAndEmptyExtensions(t *testing.T) {
	t.Parallel()
	var candidate boundcontract.Draft
	if err := json.Unmarshal(
		[]byte(`{"name":"researcher","enabled":false,"systemPrompt":"prompt","extensions":[]}`),
		&candidate,
	); err != nil {
		t.Fatal(err)
	}
	if candidate.Enabled || candidate.Extensions == nil ||
		len(candidate.Extensions) != 0 {
		t.Fatalf("Draft = %#v", candidate)
	}
	rawValue, err := json.Marshal(
		boundcontract.Draft{
			Name:         "researcher",
			SystemPrompt: "prompt",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rawValue), `"extensions":[]`) {
		t.Fatalf("Draft JSON = %s", rawValue)
	}
}

func TestDefinitionJSONRequiresIntegerRevision(t *testing.T) {
	t.Parallel()
	for _, payload := range []string{
		`{"name":"researcher","enabled":true,"systemPrompt":"prompt","extensions":[]}`,
		`{"name":"researcher","revision":null,"enabled":true,"systemPrompt":"prompt","extensions":[]}`,
		`{"name":"researcher","revision":1.5,"enabled":true,"systemPrompt":"prompt","extensions":[]}`,
	} {
		var definitionValue boundcontract.Definition
		if err := json.Unmarshal([]byte(payload), &definitionValue); err == nil {
			t.Fatalf("invalid Definition JSON was accepted: %s", payload)
		}
	}
}

func TestDefinitionValidationAndSnapshotIsolation(t *testing.T) {
	t.Parallel()
	invalidTokens := 0
	invalidDepth := int64(-1)
	testCases := []struct {
		caseName  string
		candidate boundcontract.Draft
		revision  int64
	}{
		{
			caseName: "empty name",
			candidate: boundcontract.Draft{
				SystemPrompt: "prompt",
			},
			revision: 1,
		},
		{
			caseName: "empty prompt",
			candidate: boundcontract.Draft{
				Name: "researcher",
			},
			revision: 1,
		},
		{
			caseName: "invalid tokens",
			candidate: boundcontract.Draft{
				Name:         "researcher",
				SystemPrompt: "prompt",
				AgentOptions: &agent.Options{
					MaxTokens: &invalidTokens,
				},
			},
			revision: 1,
		},
		{
			caseName: "invalid depth",
			candidate: boundcontract.Draft{
				Name:         "researcher",
				SystemPrompt: "prompt",
				MaxDepth:     &invalidDepth,
			},
			revision: 1,
		},
		{
			caseName: "empty restriction",
			candidate: boundcontract.Draft{
				Name:            "researcher",
				SystemPrompt:    "prompt",
				ToolRestriction: &tools.ToolRestriction{},
			},
			revision: 1,
		},
		{
			caseName: "duplicate extension",
			candidate: boundcontract.Draft{
				Name:         "researcher",
				SystemPrompt: "prompt",
				Extensions:   []string{"report", "report"},
			},
			revision: 1,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.caseName, func(t *testing.T) {
			t.Parallel()
			if _, err := boundcontract.NewDefinition(
				testCase.candidate,
				testCase.revision,
			); err == nil {
				t.Fatal("invalid Definition was accepted")
			}
		})
	}
	maximumTokens := 64
	allow := []string{"search"}
	extensions := []string{"report"}
	candidate := boundcontract.Draft{
		Name:         "researcher",
		Enabled:      true,
		SystemPrompt: "prompt",
		AgentOptions: &agent.Options{
			MaxTokens: &maximumTokens,
		},
		ToolRestriction: &tools.ToolRestriction{
			Allow: allow,
		},
		Extensions: extensions,
	}
	definitionValue, err := boundcontract.NewDefinition(candidate, 1)
	if err != nil {
		t.Fatal(err)
	}
	maximumTokens = 1
	allow[0] = "changed"
	extensions[0] = "changed"
	if *definitionValue.AgentOptions.MaxTokens != 64 ||
		definitionValue.ToolRestriction.Allow[0] != "search" ||
		definitionValue.Extensions[0] != "report" {
		t.Fatalf("Definition retained caller-owned storage: %#v", definitionValue)
	}
	emptyAllow, err := boundcontract.NewDefinition(
		boundcontract.Draft{
			Name:         "disabled-tools",
			SystemPrompt: "prompt",
			ToolRestriction: &tools.ToolRestriction{
				Allow: []string{},
			},
		},
		1,
	)
	if err != nil {
		t.Fatal(err)
	}
	if emptyAllow.ToolRestriction.Allow == nil ||
		len(emptyAllow.ToolRestriction.Allow) != 0 {
		t.Fatalf("empty allow semantics were lost: %#v", emptyAllow)
	}
}

func TestCommandsAndEventTypesRejectRemovedContracts(t *testing.T) {
	t.Parallel()
	var replacement boundcontract.Replacement
	if err := json.Unmarshal(
		[]byte(`{"expectedRevision":1,"definition":{"name":"researcher","enabled":true,"systemPrompt":"prompt","extensions":[]}}`),
		&replacement,
	); err != nil {
		t.Fatal(err)
	}
	if replacement.ExpectedRevision != 1 ||
		replacement.Definition.Name != "researcher" {
		t.Fatalf("replacement = %#v", replacement)
	}
	for _, eventType := range []string{
		boundcontract.BindingEventName,
		boundcontract.DefinitionAppliedEventName,
		boundcontract.MaterializationEventName,
		boundcontract.CursorEventName,
	} {
		if !session.IsKnownEventType(eventType) {
			t.Fatalf("Bound event type %q is not registered", eventType)
		}
	}
	for _, removedEventType := range []string{
		"subagent/bound-config",
		"subagent/bound-config-applied",
	} {
		if session.IsKnownEventType(removedEventType) {
			t.Fatalf("removed Bound event type %q is registered", removedEventType)
		}
	}
}
