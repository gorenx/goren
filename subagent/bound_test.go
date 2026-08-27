package subagent_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
)

func TestBoundPersistedContractsRoundTripStrictly(t *testing.T) {
	t.Parallel()
	maxTokens := 1024
	creation := subagent.BoundCreation{
		SeedBuilder: "spawn",
		Title:       "researcher",
		InitialPrompt: []agentmessage.ContentBlock{
			agentmessage.NewTextBlock("start"),
		},
		AgentOptions: agent.Options{
			Provider:  "provider",
			Model:     "model",
			MaxTokens: &maxTokens,
		},
	}
	rawValue, err := json.Marshal(creation)
	if err != nil {
		t.Fatal(err)
	}
	var decoded subagent.BoundCreation
	if err = json.Unmarshal(rawValue, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.SeedBuilder != creation.SeedBuilder ||
		decoded.Title != creation.Title ||
		decoded.AgentOptions.Provider != creation.AgentOptions.Provider ||
		decoded.AgentOptions.Model != creation.AgentOptions.Model ||
		decoded.AgentOptions.MaxTokens == nil ||
		*decoded.AgentOptions.MaxTokens != maxTokens ||
		len(decoded.InitialPrompt) != 1 {
		t.Fatalf("decoded Bound creation = %#v", decoded)
	}
	if err = json.Unmarshal(
		[]byte(`{"seedBuilder":"spawn","title":"researcher","initialPrompt":[],"agentProvider":"","agentModel":"","extra":true}`),
		&decoded,
	); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown-field error = %v", err)
	}
}

func TestBoundCreationRequiresInitialPromptAndValidAgentOptions(t *testing.T) {
	t.Parallel()
	invalidMaxTokens := 0
	for _, creation := range []subagent.BoundCreation{
		{
			SeedBuilder: "spawn",
			Title:       "researcher",
		},
		{
			SeedBuilder: "spawn",
			Title:       "researcher",
			InitialPrompt: []agentmessage.ContentBlock{
				agentmessage.NewTextBlock("start"),
			},
			AgentOptions: agent.Options{
				MaxTokens: &invalidMaxTokens,
			},
		},
	} {
		if _, err := json.Marshal(creation); err == nil {
			t.Fatalf("invalid Bound creation was accepted: %#v", creation)
		}
	}
}

func TestBoundEventTypesAreRegistered(t *testing.T) {
	t.Parallel()
	for _, eventType := range []string{
		subagent.BoundBindingEventName,
		subagent.BoundConfigEventName,
		subagent.BoundConfigAppliedEventName,
		subagent.BoundMaterializationEventName,
		subagent.BoundCursorEventName,
	} {
		if !session.IsKnownEventType(eventType) {
			t.Fatalf("Bound event type %q is not registered", eventType)
		}
	}
}
