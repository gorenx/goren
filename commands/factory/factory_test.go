package factory

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gorenx/goren/commands"
)

func TestFactoryStrictlyConstructsCommandsPlugin(t *testing.T) {
	t.Parallel()
	builder := New(commands.RuntimeOptions{
		InstanceToken: "factory",
	})
	instance, err := builder.Create(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	metadata := instance.Manifest()
	if metadata.Name != commands.PluginName || len(metadata.Provides) != 1 {
		t.Fatalf("Commands manifest = %#v", metadata)
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
