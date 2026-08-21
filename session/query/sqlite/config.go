// Package sqlite implements the disposable SQLite FTS5 index consumed by
// Session Query. It never owns canonical Session facts.
package sqlite

import (
	"fmt"
	"strings"

	sessionquery "github.com/gorenx/goren/session/query"
)

// JournalMode is the supported derived-index journal policy.
type JournalMode string

const (
	JournalWAL      JournalMode = "wal"
	JournalDelete   JournalMode = "delete"
	JournalTruncate JournalMode = "truncate"
	JournalPersist  JournalMode = "persist"
)

// Config identifies one disposable search index.
type Config struct {
	Path              string
	JournalMode       JournalMode
	SnippetCodePoints int
}

func (settings Config) validate() (Config, error) {
	if strings.TrimSpace(settings.Path) == "" {
		return Config{}, fmt.Errorf("session query sqlite: path is empty")
	}
	if settings.JournalMode == "" {
		settings.JournalMode = JournalWAL
	}
	switch settings.JournalMode {
	case JournalWAL, JournalDelete, JournalTruncate, JournalPersist:
	default:
		return Config{}, fmt.Errorf("session query sqlite: unsupported journal mode %q", settings.JournalMode)
	}
	if settings.SnippetCodePoints == 0 {
		settings.SnippetCodePoints = sessionquery.DefaultSnippetSize
	}
	if settings.SnippetCodePoints < 1 {
		return Config{}, fmt.Errorf("session query sqlite: snippetCodePoints must be positive")
	}
	return settings, nil
}
