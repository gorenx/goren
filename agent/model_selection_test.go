package agent_test

import (
	"encoding/json"
	agentcore "github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	"testing"
)

func TestModelSelectionUsesExplicitThenRecordedThenDefault(t *testing.T) {
	explicit := agentcore.ModelSelection{
		Provider: "explicit",
		Model:    "selected",
	}
	defaults := agentcore.ModelSelection{
		Provider: "default",
		Model:    "fallback",
	}
	recorded := agentcore.ModelSelection{
		Provider: "recorded",
		Model:    "previous",
	}
	encoded, err := json.Marshal(session.RequestHeaderSnapshot{
		Header: session.EpochHeader{
			Config: llm.CallConfig{
				Provider: recorded.Provider,
				Model:    recorded.Model,
			},
		},
		Reason: session.RequestHeaderInitial,
	})
	if err != nil {
		t.Fatal(err)
	}
	entries := []session.Event{
		{
			Seq:  0,
			Type: session.RequestHeaderEventName,
			Data: encoded,
		},
	}
	for _, testCase := range []struct {
		name      string
		requested *agentcore.ModelSelection
		events    []session.Event
		fallback  agentcore.ModelSelection
		want      *agentcore.ModelSelection
	}{
		{
			name:      "explicit",
			requested: &explicit,
			events:    entries,
			fallback:  defaults,
			want:      &explicit,
		},
		{
			name:     "recorded",
			events:   entries,
			fallback: defaults,
			want:     &recorded,
		},
		{
			name:     "default",
			fallback: defaults,
			want:     &defaults,
		},
		{
			name: "absent",
		},
		{
			name: "incomplete default",
			fallback: agentcore.ModelSelection{
				Provider: "alone",
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := agentcore.ResolveModelSelection(testCase.requested, testCase.events, testCase.fallback)
			if err != nil {
				t.Fatal(err)
			}
			if testCase.want == nil {
				if got != nil {
					t.Fatalf("selection = %#v", got)
				}
				return
			}
			if got == nil || *got != *testCase.want {
				t.Fatalf("selection = %#v, want %#v", got, testCase.want)
			}
			got.Model = "mutated"
			if testCase.want.Model == "mutated" {
				t.Fatal("result aliases input")
			}
		})
	}
}

func TestModelSelectionDoesNotHideRecoveryErrors(t *testing.T) {
	events := []session.Event{
		{
			Type: session.RequestHeaderEventName,
			Data: json.RawMessage(`{`),
		},
	}
	if _, err := agentcore.ResolveModelSelection(nil, events, agentcore.ModelSelection{}); err == nil {
		t.Fatal("invalid log became an absent selection")
	}
	explicit := agentcore.ModelSelection{
		Provider: "explicit",
		Model:    "selected",
	}
	got, err := agentcore.ResolveModelSelection(&explicit, events, agentcore.ModelSelection{})
	if err != nil || got == nil || *got != explicit {
		t.Fatal("explicit choice unnecessarily read fallback")
	}
}
