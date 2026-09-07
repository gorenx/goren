package delegation

import (
	"encoding/json"
	"testing"
)

func TestDecodeConfigMapsUnspecifiedDepthToNoLocalLimit(t *testing.T) {
	t.Parallel()
	settings, decodeErr := DecodeConfig(json.RawMessage(`{
  "provider": "spawn",
  "maxDepth": "provider-managed"
}`))
	if decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if settings.MaxDepth != nil {
		t.Fatalf("provider-managed maxDepth = %d", *settings.MaxDepth)
	}
}

func TestDecodeConfigPreservesExplicitEmptyRestrictions(t *testing.T) {
	testCases := []struct {
		name         string
		input        string
		allowPresent bool
		denyPresent  bool
	}{
		{
			name:         "empty allow with deny",
			input:        `{"provider":"spawn","toolFilter":{"allow":[],"deny":["write"]}}`,
			allowPresent: true,
			denyPresent:  true,
		},
		{
			name:         "empty allow",
			input:        `{"provider":"spawn","toolFilter":{"allow":[]}}`,
			allowPresent: true,
		},
		{
			name:        "empty deny",
			input:       `{"provider":"spawn","toolFilter":{"deny":[]}}`,
			denyPresent: true,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			resolved, err := DecodeConfig(json.RawMessage(testCase.input))
			if err != nil {
				t.Fatal(err)
			}
			if resolved.ToolFilter == nil || (resolved.ToolFilter.Allow != nil) != testCase.allowPresent || (resolved.ToolFilter.Deny != nil) != testCase.denyPresent {
				t.Fatalf("filter presence changed: %#v", resolved.ToolFilter)
			}
			if len(resolved.ToolFilter.Allow) != 0 {
				t.Fatal("empty allow changed")
			}
		})
	}
}

func TestDecodeConfigRejectsInvalidObjects(t *testing.T) {
	for _, rawValue := range []string{
		``,
		`null`,
		`[]`,
		`{"provider":"spawn","unknown":true}`,
		`{"provider":"spawn","provider":"other"}`,
		`{"provider":"spawn","agentOptions":{"model":"a","model":"b"}}`,
		`{"provider":"spawn"} {}`,
		`{"provider":"spawn","maxDepth":-1}`,
		`{"provider":"spawn","maxDepth":-0}`,
	} {
		t.Run(rawValue, func(t *testing.T) {
			if _, err := DecodeConfig(json.RawMessage(rawValue)); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}
