package approval

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gorenx/goren/session"
)

func TestPolicyResolutionAndText(t *testing.T) {
	scenarios := []struct {
		name     string
		fallback Policy
		entries  []session.Event
		want     Policy
		failure  bool
	}{
		{
			name:     "default",
			fallback: PolicyNever,
			want:     PolicyNever,
		},
		{
			name:     "last override wins",
			fallback: PolicyAsk,
			entries: []session.Event{
				{
					Type: PolicyEventName,
					Data: json.RawMessage(`{"policy":"ask"}`),
				},
				{
					Type: PolicyEventName,
					Data: json.RawMessage(`{"policy":"never","source":"delegation"}`),
				},
			},
			want: PolicyNever,
		},
		{
			name:     "unrelated event ignored",
			fallback: PolicyAsk,
			entries: []session.Event{
				{
					Type: "other",
					Data: json.RawMessage(`invalid`),
				},
			},
			want: PolicyAsk,
		},
		{
			name:     "invalid latest does not fall back",
			fallback: PolicyAsk,
			entries: []session.Event{
				{
					Type: PolicyEventName,
					Data: json.RawMessage(`{"policy":"never"}`),
				},
				{
					Type: PolicyEventName,
					Data: json.RawMessage(`{"policy":"invalid"}`),
				},
			},
			failure: true,
		},
		{
			name:     "invalid source",
			fallback: PolicyAsk,
			entries: []session.Event{
				{
					Type: PolicyEventName,
					Data: json.RawMessage(`{"policy":"never","source":"invalid"}`),
				},
			},
			failure: true,
		},
		{
			name:     "malformed event",
			fallback: PolicyAsk,
			entries: []session.Event{
				{
					Type: PolicyEventName,
					Data: json.RawMessage(`{`),
				},
			},
			failure: true,
		},
		{
			name:    "unvalidated default",
			failure: true,
		},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			selectedPolicy, err := scenario.fallback.Resolve(scenario.entries)
			if scenario.failure {
				if err == nil {
					t.Fatal("invalid policy was accepted")
				}
				return
			}
			if err != nil || selectedPolicy != scenario.want {
				t.Fatalf("policy = %q, %v", selectedPolicy, err)
			}
			textValue, err := selectedPolicy.Text()
			if err != nil {
				t.Fatal(err)
			}
			if selectedPolicy == PolicyAsk && !strings.Contains(textValue, "Approval policy: ask.") {
				t.Fatal(textValue)
			}
			if selectedPolicy == PolicyNever && !strings.Contains(textValue, "rejected automatically") {
				t.Fatal(textValue)
			}
		})
	}
	if _, err := Policy("invalid").Text(); err == nil {
		t.Fatal("invalid policy produced text")
	}
}

func TestPolicyConfigInheritanceCopiesValues(t *testing.T) {
	parentPolicy := PolicyNever
	parent, err := ValidateConfig(Config{
		Policy: &parentPolicy,
	})
	if err != nil {
		t.Fatal(err)
	}
	child, err := parent.WithOverrides(Config{})
	if err != nil {
		t.Fatal(err)
	}
	parentPolicy = PolicyAsk
	if child.policy != PolicyNever {
		t.Fatal("child borrowed parent configuration pointer")
	}
	customPolicy := PolicyAsk
	customized, err := parent.WithOverrides(Config{
		Policy: &customPolicy,
	})
	if err != nil {
		t.Fatal(err)
	}
	customPolicy = PolicyNever
	if customized.policy != PolicyAsk || parent.policy != PolicyNever {
		t.Fatal("override changed inherited value or retained pointer")
	}
	invalid := Policy("invalid")
	if _, err := parent.WithOverrides(Config{
		Policy: &invalid,
	}); err == nil {
		t.Fatal("invalid override accepted")
	}
	if _, err := (ValidatedConfig{}).WithOverrides(Config{}); err == nil {
		t.Fatal("unvalidated parent accepted")
	}
}
