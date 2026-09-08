package sqlite

import (
	"encoding/json"
	"testing"
)

func TestDecodeConfig(t *testing.T) {
	for _, raw := range []string{`null`, `[]`, `{}`, `{"path":""}`, `{"path":"x","journalMode":"memory"}`, `{"path":"x","unknown":true}`, `{"path":"x","path":"y"}`, `{"path":"x"}{}`} {
		if _, err := DecodeConfig(json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	settings, err := DecodeConfig(json.RawMessage(`{"path":"workspaces.sqlite"}`))
	if err != nil || settings.JournalMode != JournalWAL {
		t.Fatalf("settings = %+v, %v", settings, err)
	}
}
