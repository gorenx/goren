package sqlite

import (
	"fmt"
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
