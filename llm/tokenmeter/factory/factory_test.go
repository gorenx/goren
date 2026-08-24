package factory

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gorenx/goren/llm/tokenmeter"
)

func TestFactoryAcceptsOnlyEmptyConfiguration(t *testing.T) {
	t.Parallel()
	owner := New()
	instance, err := owner.Create(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if instance.Manifest().Name != tokenmeter.PluginName {
		t.Fatalf("plugin manifest = %#v", instance.Manifest())
	}
	for _, rawValue := range []json.RawMessage{
		json.RawMessage(`{"estimate":true}`),
		json.RawMessage(`null`),
		json.RawMessage(`{} {}`),
	} {
		if _, err := owner.Create(context.Background(), rawValue); err == nil {
			t.Fatalf("configuration %s was accepted", rawValue)
		}
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := owner.Create(cancelled, json.RawMessage(`{}`)); err == nil ||
		!strings.Contains(err.Error(), "canceled") {
		t.Fatalf("cancelled Create error = %v", err)
	}
}
