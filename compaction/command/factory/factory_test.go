package factory

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gorenx/goren/compaction/command"
)

func TestFactoryStrictlyConstructsCompactCommandPlugin(t *testing.T) {
	t.Parallel()
	builder := New()
	instance, err := builder.Create(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	metadata := instance.Manifest()
	if metadata.Name != command.PluginName || len(metadata.Requires) != 2 {
		t.Fatalf("command-compact manifest = %#v", metadata)
	}
	for _, rawConfig := range []json.RawMessage{
		json.RawMessage(`{"unknown":true}`),
		json.RawMessage(`[]`),
		json.RawMessage(`{"x":1,"x":2}`),
	} {
		if _, err := builder.Create(context.Background(), rawConfig); err == nil {
			t.Fatalf("configuration %s succeeded", rawConfig)
		}
	}
	cancelledContext, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := builder.Create(cancelledContext, json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("cancelled Create error = %v", err)
	}
}
