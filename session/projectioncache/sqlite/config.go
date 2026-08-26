// Package sqlite implements the disposable SQLite checkpoint store consumed by
// Session Projection Cache. It never owns Session facts or projection policy.
package sqlite

import (
	"fmt"
	"strings"
)

// JournalMode is the supported checkpoint database journal policy.
type JournalMode string

const (
	JournalWAL      JournalMode = "wal"
	JournalDelete   JournalMode = "delete"
	JournalTruncate JournalMode = "truncate"
	JournalPersist  JournalMode = "persist"
)

// Config identifies one disposable projection checkpoint database.
type Config struct {
	Path        string
	JournalMode JournalMode
}

func (settings Config) validate() (Config, error) {
	if strings.TrimSpace(settings.Path) == "" {
		return Config{}, fmt.Errorf("session projection cache sqlite: path is empty")
	}
	if settings.JournalMode == "" {
		settings.JournalMode = JournalWAL
	}
	switch settings.JournalMode {
	case JournalWAL, JournalDelete, JournalTruncate, JournalPersist:
	default:
		return Config{}, fmt.Errorf(
			"session projection cache sqlite: unsupported journal mode %q",
			settings.JournalMode,
		)
	}
	return settings, nil
}
