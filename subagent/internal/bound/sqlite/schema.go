package sqlite

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

const (
	schemaVersion = 1
	applicationID = 0x47424E44
)

//go:embed sql/schema.sql
var schemaDDL string

// Key is a table name owned by this adapter. The empty value records
// membership only when validating database ownership.
var recognizedTables = map[string]struct{}{
	"bound_definitions": {},
}

func openDatabase(
	requestContext context.Context,
	settings Config,
) (*sql.DB, error) {
	if requestContext == nil {
		return nil, errors.New(
			"subagent Bound Definition sqlite: Open Context is nil",
		)
	}
	resolved, err := settings.validate()
	if err != nil {
		return nil, err
	}
	actualPath := resolved.Path
	if actualPath != ":memory:" {
		actualPath, err = filepath.Abs(actualPath)
		if err != nil {
			return nil, err
		}
		if err = os.MkdirAll(filepath.Dir(actualPath), 0o700); err != nil {
			return nil, fmt.Errorf(
				"subagent Bound Definition sqlite: create database directory: %w",
				err,
			)
		}
		fileHandle, createErr := os.OpenFile(
			actualPath,
			os.O_CREATE|os.O_EXCL|os.O_WRONLY,
			0o600,
		)
		if createErr == nil {
			createErr = fileHandle.Close()
		}
		if createErr != nil && !errors.Is(createErr, os.ErrExist) {
			return nil, fmt.Errorf(
				"subagent Bound Definition sqlite: create database file: %w",
				createErr,
			)
		}
	}
	database, err := sql.Open("sqlite", actualPath)
	if err != nil {
		return nil, err
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	if err = configureDatabase(
		requestContext,
		database,
		actualPath,
		resolved.JournalMode,
	); err != nil {
		_ = database.Close()
		return nil, err
	}
	return database, nil
}

func configureDatabase(
	requestContext context.Context,
	database *sql.DB,
	actualPath string,
	mode JournalMode,
) error {
	connection, err := database.Conn(requestContext)
	if err != nil {
		return err
	}
	defer connection.Close()
	if _, err = connection.ExecContext(
		requestContext,
		"PRAGMA busy_timeout = 5000",
	); err != nil {
		return err
	}
	if _, err = connection.ExecContext(
		requestContext,
		"BEGIN IMMEDIATE",
	); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = connection.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	var onDisk int
	if err = connection.QueryRowContext(
		requestContext,
		"PRAGMA user_version",
	).Scan(&onDisk); err != nil {
		return err
	}
	var storedApplicationID int
	if err = connection.QueryRowContext(
		requestContext,
		"PRAGMA application_id",
	).Scan(&storedApplicationID); err != nil {
		return err
	}
	tables, err := listTables(requestContext, connection)
	if err != nil {
		return err
	}
	if storedApplicationID != 0 && storedApplicationID != applicationID {
		return fmt.Errorf(
			"subagent Bound Definition sqlite: database %q belongs to another application",
			actualPath,
		)
	}
	if storedApplicationID == 0 && (onDisk != 0 || len(tables) != 0) {
		return fmt.Errorf(
			"subagent Bound Definition sqlite: database %q is not empty",
			actualPath,
		)
	}
	if storedApplicationID == applicationID {
		if onDisk != schemaVersion {
			return fmt.Errorf(
				"subagent Bound Definition sqlite: unsupported schema version %d",
				onDisk,
			)
		}
		for _, tableName := range tables {
			if _, recognized := recognizedTables[tableName]; !recognized {
				return fmt.Errorf(
					"subagent Bound Definition sqlite: database %q contains unrecognized table %q",
					actualPath,
					tableName,
				)
			}
		}
	}
	if _, err = connection.ExecContext(requestContext, schemaDDL); err != nil {
		return err
	}
	if storedApplicationID == 0 {
		if _, err = connection.ExecContext(
			requestContext,
			fmt.Sprintf("PRAGMA application_id = %d", applicationID),
		); err != nil {
			return err
		}
		if _, err = connection.ExecContext(
			requestContext,
			fmt.Sprintf("PRAGMA user_version = %d", schemaVersion),
		); err != nil {
			return err
		}
	}
	if _, err = connection.ExecContext(requestContext, "COMMIT"); err != nil {
		return err
	}
	committed = true
	if _, err = connection.ExecContext(
		requestContext,
		"PRAGMA journal_mode = "+strings.ToUpper(string(mode)),
	); err != nil {
		return err
	}
	_, err = connection.ExecContext(
		requestContext,
		"PRAGMA synchronous = NORMAL",
	)
	return err
}

type rowQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func listTables(
	requestContext context.Context,
	source rowQuerier,
) ([]string, error) {
	rows, err := source.QueryContext(
		requestContext,
		"SELECT name FROM sqlite_schema WHERE type = 'table' AND name NOT GLOB 'sqlite_*' ORDER BY name",
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var tableName string
		if err = rows.Scan(&tableName); err != nil {
			return nil, err
		}
		result = append(result, tableName)
	}
	return result, rows.Err()
}
