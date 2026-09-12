// Package sqlite implements Settings persistence without assigning configuration
// meaning, merge rules or application behavior.
package sqlite

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"net/url"
	"os"
	"path/filepath"

	"github.com/gorenx/goren/settings"
	"github.com/gorenx/goren/settings/internal/sqlite/internal/dbsql"
	_ "modernc.org/sqlite"
)

//go:embed sql/schema.sql
var schema string

// Adapter owns database access. The module installer closes it after work drains.
type Adapter struct {
	database *sql.DB
	queries  *dbsql.Queries
}

// Open creates or validates the Settings database. Schema version one is the
// initial migration; unknown existing versions fail without altering tables.
func Open(ctx context.Context, path string) (*Adapter, error) {
	if path == "" || !filepath.IsAbs(path) {
		return nil, settings.ErrInvalid
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, settings.ErrStorage
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err == nil {
		if err := file.Close(); err != nil {
			return nil, settings.ErrStorage
		}
	} else if !errors.Is(err, os.ErrExist) {
		return nil, settings.ErrStorage
	}
	opened, err := openConnection(path, false)
	if err != nil {
		return nil, err
	}
	if err := opened.migrate(ctx); err != nil {
		_ = opened.Close()
		return nil, err
	}
	return opened, nil
}

// Inspect reads an existing database without creating any file or migrating it.
// WAL databases are rejected because a read might require new shared-memory files.
func Inspect(ctx context.Context, path string) (settings.State, error) {
	if path == "" || !filepath.IsAbs(path) {
		return settings.State{}, settings.ErrInvalid
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return settings.State{}, nil
	}
	if err != nil {
		return settings.State{}, settings.ErrStorage
	}
	header := make([]byte, 100)
	count, readErr := file.Read(header)
	closeErr := file.Close()
	if readErr != nil ||
		closeErr != nil ||
		count != len(header) ||
		header[18] == 2 ||
		header[19] == 2 {
		return settings.State{}, settings.ErrStorage
	}
	opened, err := openConnection(path, true)
	if err != nil {
		return settings.State{}, err
	}
	defer opened.Close()
	return opened.Load(ctx)
}

func openConnection(path string, readonly bool) (*Adapter, error) {
	location := url.URL{
		Scheme: "file",
		Path:   filepath.Clean(path),
	}
	parameters := url.Values{}
	parameters.Set("mode", "rw")
	if readonly {
		parameters.Set("mode", "ro")
		parameters.Add("_pragma", "query_only(1)")
	}
	parameters.Add("_pragma", "busy_timeout(5000)")
	parameters.Add("_pragma", "synchronous(FULL)")
	location.RawQuery = parameters.Encode()
	database, err := sql.Open("sqlite", location.String())
	if err != nil {
		return nil, settings.ErrStorage
	}
	database.SetMaxOpenConns(1)
	return &Adapter{
		database: database,
		queries:  dbsql.New(database),
	}, nil
}

func (owner *Adapter) migrate(ctx context.Context) error {
	transaction, err := owner.database.BeginTx(ctx, nil)
	if err != nil {
		return settings.ErrStorage
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(ctx, schema); err != nil {
		return settings.ErrStorage
	}
	queries := owner.queries.WithTx(transaction)
	if err := queries.InitializeState(ctx); err != nil {
		return settings.ErrStorage
	}
	row, err := queries.ReadState(ctx)
	if err != nil || row.SchemaVersion != 1 {
		return settings.ErrStorage
	}
	if err := transaction.Commit(); err != nil {
		return settings.ErrStorage
	}
	return nil
}

// Close releases database resources; database/sql supports repeated closure.
func (owner *Adapter) Close() error {
	if err := owner.database.Close(); err != nil {
		return settings.ErrStorage
	}
	return nil
}
