package compaction_test

import (
	"encoding/json"
	"testing"

	"github.com/gorenx/goren/compaction"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
)

func TestDefinitionRegistersCanonicalEvents(t *testing.T) {
	t.Parallel()
	for _, eventName := range []string{
		compaction.StartEventName,
		compaction.SummaryEventName,
		compaction.EndEventName,
		compaction.PruneEventName,
	} {
		if !session.IsKnownEventType(eventName) {
			t.Fatalf("event %q is not registered", eventName)
		}
	}
}

func TestCheckpointSourceSurvivesMessageRoundTrip(t *testing.T) {
	t.Parallel()
	origin, err := compaction.NewCheckpointSource("compact-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	message, err := llm.NewUserMessage(llm.UserMessageInput{
		Content: []llm.ContentBlock{
			llm.NewTextBlock("checkpoint"),
		},
		Source: origin,
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := llm.DecodeUserMessage(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !compaction.IsCheckpointSource(restored.SourceValue()) {
		t.Fatalf("restored source = %#v", restored.SourceValue())
	}
}
