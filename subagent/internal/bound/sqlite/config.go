// Package sqlite persists global Bound Definitions in an independent SQLite
// database. It owns rows and schema mechanics, not Definition decisions.
package sqlite

import (
	"fmt"
	"strings"
)

// JournalMode is the supported Bound Definition database journal policy.
type JournalMode string

const (
	JournalWAL      JournalMode = "wal"
	JournalDelete   JournalMode = "delete"
	JournalTruncate JournalMode = "truncate"
	JournalPersist  JournalMode = "persist"
)

// Config identifies one Bound Definition database.
type Config struct {
	Path        string
	JournalMode JournalMode
}

func (settings Config) validate() (Config, error) {
	if strings.TrimSpace(settings.Path) == "" {
		return Config{}, fmt.Errorf(
			"subagent Bound Definition sqlite: path is empty",
		)
	}
	if settings.JournalMode == "" {
		settings.JournalMode = JournalWAL
	}
	switch settings.JournalMode {
	case JournalWAL, JournalDelete, JournalTruncate, JournalPersist:
	default:
		return Config{}, fmt.Errorf(
			"subagent Bound Definition sqlite: unsupported journal mode %q",
			settings.JournalMode,
		)
	}
	return settings, nil
}
