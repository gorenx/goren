package factory

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gorenx/goren/session/projectioncache"
)

type failureSink struct{}

func (failureSink) ReportProjectionCacheFailure(projectioncache.Failure) {}

func TestFactoryCreatesProjectionCachePlugin(t *testing.T) {
	builder, err := New(failureSink{})
	if err != nil {
		t.Fatal(err)
	}
	instance, err := builder.Create(
		context.Background(),
		json.RawMessage(`{"path":"/tmp/goren-projection-cache.sqlite"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if instance.Manifest().Name != projectioncache.PluginName {
		t.Fatalf("Manifest name = %q", instance.Manifest().Name)
	}
}

func TestFactoryRejectsInvalidConfiguration(t *testing.T) {
	builder, err := New(failureSink{})
	if err != nil {
		t.Fatal(err)
	}
	testCases := []json.RawMessage{
		json.RawMessage(`{"path":"","journalMode":"wal"}`),
		json.RawMessage(`{"path":"/tmp/cache.sqlite","journalMode":"memory"}`),
		json.RawMessage(`{"path":"/tmp/cache.sqlite","writeEveryEvents":-1}`),
		json.RawMessage(`{"path":"/tmp/cache.sqlite","writeIntervalMs":-1}`),
		json.RawMessage(`{"path":"/tmp/cache.sqlite","unknown":true}`),
	}
	for _, rawConfig := range testCases {
		if _, err := builder.Create(context.Background(), rawConfig); err == nil {
			t.Fatalf("Create accepted %s", rawConfig)
		}
	}
}

func TestNewRequiresFailureReporter(t *testing.T) {
	if _, err := New(nil); err == nil {
		t.Fatal("New accepted a nil failure reporter")
	}
}
