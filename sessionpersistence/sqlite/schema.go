package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

const (
	schemaVersion = 15
	applicationID = 0x44534850
)

//go:embed sql/schema.sql
var schemaDDL string

func openDatabase(requestContext context.Context, settings Config) (*sql.DB, string, error) {
	if err := settings.validate(); err != nil {
		return nil, "", err
	}
	actualPath := settings.Path
	if actualPath != ":memory:" {
		resolved, err := filepath.Abs(actualPath)
		if err != nil {
			return nil, "", err
		}
		actualPath = resolved
		if err := os.MkdirAll(filepath.Dir(actualPath), 0o700); err != nil {
			return nil, "", fmt.Errorf("session persistence sqlite: create database directory: %w", err)
		}
		fileHandle, err := os.OpenFile(actualPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			err = fileHandle.Close()
		}
		if err != nil && !errors.Is(err, os.ErrExist) {
			return nil, "", fmt.Errorf("session persistence sqlite: create database file: %w", err)
		}
	}
	database, err := sql.Open("sqlite", actualPath)
	if err != nil {
		return nil, "", err
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	if err := configureDatabase(requestContext, database, actualPath, settings.JournalMode); err != nil {
		_ = database.Close()
		return nil, "", err
	}
	var storeID string
	if err := database.QueryRowContext(requestContext,
		"SELECT store_id FROM persistence_state WHERE singleton = 1").Scan(&storeID); err != nil {
		_ = database.Close()
		return nil, "", fmt.Errorf("session persistence sqlite: read store identity: %w", err)
	}
	if storeID == "" {
		_ = database.Close()
		return nil, "", errors.New("session persistence sqlite: store identity is empty")
	}
	return database, "sqlite:" + actualPath + ":store:" + storeID, nil
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
	if _, err := connection.ExecContext(requestContext, "PRAGMA foreign_keys = ON"); err != nil {
		return err
	}
	if _, err := connection.ExecContext(requestContext, "PRAGMA busy_timeout = 5000"); err != nil {
		return err
	}
	if _, err := connection.ExecContext(requestContext, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = connection.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	var onDisk int
	if err := connection.QueryRowContext(requestContext, "PRAGMA user_version").Scan(&onDisk); err != nil {
		return err
	}
	var storedApplicationID int
	if err := connection.QueryRowContext(requestContext, "PRAGMA application_id").Scan(&storedApplicationID); err != nil {
		return err
	}
	var userObjectCount int
	if err := connection.QueryRowContext(requestContext,
		"SELECT COUNT(*) FROM sqlite_schema WHERE name NOT GLOB 'sqlite_*'").Scan(&userObjectCount); err != nil {
		return err
	}
	if onDisk == 0 && (storedApplicationID != 0 || userObjectCount > 0) {
		return fmt.Errorf("session persistence sqlite: database %q has an unversioned schema or application identity", actualPath)
	}
	if onDisk != 0 && onDisk != schemaVersion {
		return fmt.Errorf(
			"session persistence sqlite: database %q has schema version %d, expected %d",
			actualPath, onDisk, schemaVersion,
		)
	}
	if onDisk == schemaVersion && storedApplicationID != applicationID {
		return fmt.Errorf(
			"session persistence sqlite: database %q has application id %d, expected %d",
			actualPath, storedApplicationID, applicationID,
		)
	}
	if _, err := connection.ExecContext(requestContext, schemaDDL); err != nil {
		return err
	}
	storeID, err := randomUUID()
	if err != nil {
		return err
	}
	if _, err := connection.ExecContext(requestContext,
		"INSERT OR IGNORE INTO persistence_state (singleton, store_id) VALUES (1, ?)", storeID); err != nil {
		return err
	}
	if onDisk == 0 {
		if _, err := connection.ExecContext(requestContext, fmt.Sprintf("PRAGMA application_id = %d", applicationID)); err != nil {
			return err
		}
		if _, err := connection.ExecContext(requestContext, fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
			return err
		}
	}
	if _, err := connection.ExecContext(requestContext, "COMMIT"); err != nil {
		return err
	}
	committed = true
	pragmaMode := strings.ToUpper(string(mode))
	if _, err := connection.ExecContext(requestContext, "PRAGMA journal_mode = "+pragmaMode); err != nil {
		return err
	}
	_, err = connection.ExecContext(requestContext, "PRAGMA synchronous = FULL")
	return err
}

func randomUUID() (string, error) {
	var randomBytes [16]byte
	if _, err := rand.Read(randomBytes[:]); err != nil {
		return "", err
	}
	randomBytes[6] = randomBytes[6]&0x0f | 0x40
	randomBytes[8] = randomBytes[8]&0x3f | 0x80
	encoded := make([]byte, 36)
	hex.Encode(encoded[0:8], randomBytes[0:4])
	encoded[8] = '-'
	hex.Encode(encoded[9:13], randomBytes[4:6])
	encoded[13] = '-'
	hex.Encode(encoded[14:18], randomBytes[6:8])
	encoded[18] = '-'
	hex.Encode(encoded[19:23], randomBytes[8:10])
	encoded[23] = '-'
	hex.Encode(encoded[24:36], randomBytes[10:16])
	return string(encoded), nil
}
