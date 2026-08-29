package llm_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gorenx/goren/llm"
)

func TestStreamChunkDecodeRejectsUnknownCoreFields(t *testing.T) {
	t.Parallel()
	_, err := llm.DecodeStreamChunk(json.RawMessage(`{"type":"text-delta","index":0,"text":"x","extra":true}`))
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("decode error = %v", err)
	}
}
