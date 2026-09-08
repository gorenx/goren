package sqlite

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/gorenx/goren/internal/jsonvalue"
	"strings"
)

// JournalMode is the supported SQLite journaling vocabulary.
type JournalMode string

const (
	JournalWAL      JournalMode = "wal"
	JournalDelete   JournalMode = "delete"
	JournalTruncate JournalMode = "truncate"
	JournalPersist  JournalMode = "persist"
)

// Config identifies one Workspace SQLite data store.
type Config struct {
	Path        string
	JournalMode JournalMode
}

func (settings Config) validate() error {
	if strings.TrimSpace(settings.Path) == "" {
		return fmt.Errorf("workspace persistence sqlite: path is empty")
	}
	switch settings.JournalMode {
	case JournalWAL, JournalDelete, JournalTruncate, JournalPersist:
		return nil
	default:
		return fmt.Errorf("workspace persistence sqlite: unsupported journal mode %q", settings.JournalMode)
	}
}

// DecodeConfig validates an object before opening any storage resources.
func DecodeConfig(raw json.RawMessage) (Config, error) {
	if !jsonvalue.IsObject(raw) {
		return Config{}, fmt.Errorf("workspace sqlite: configuration must be a lossless JSON object")
	}
	var wire struct {
		Path        string      `json:"path"`
		JournalMode JournalMode `json:"journalMode,omitempty"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return Config{}, err
	}
	settings := Config{
		Path:        wire.Path,
		JournalMode: wire.JournalMode,
	}
	if settings.JournalMode == "" {
		settings.JournalMode = JournalWAL
	}
	return settings, settings.validate()
}
