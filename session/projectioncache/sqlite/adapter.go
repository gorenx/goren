package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/gorenx/goren/internal/jsonvalue"
	"github.com/gorenx/goren/session"
	sessionprojection "github.com/gorenx/goren/session/projection"
	"github.com/gorenx/goren/session/projectioncache"
	"github.com/gorenx/goren/session/projectioncache/sqlite/internal/dbsql"
)

// Adapter maps checkpoint records to one disposable SQLite database.
type Adapter struct {
	database  *sql.DB
	queries   *dbsql.Queries
	path      string
	closeOnce sync.Once
	closeErr  error
}

// Open validates database ownership and initializes the private schema.
func Open(requestContext context.Context, settings Config) (*Adapter, error) {
	if requestContext == nil {
		return nil, errors.New("session projection cache sqlite: Open Context is nil")
	}
	database, actualPath, err := openDatabase(requestContext, settings)
	if err != nil {
		return nil, err
	}
	return &Adapter{
		database: database,
		queries:  dbsql.New(database),
		path:     actualPath,
	}, nil
}

// LoadAll reads one transactionally consistent checkpoint record index.
func (owner *Adapter) LoadAll(
	requestContext context.Context,
) (map[session.SessionID]projectioncache.CheckpointRecord, error) {
	transaction, err := owner.database.BeginTx(
		requestContext,
		&sql.TxOptions{
			ReadOnly: true,
		},
	)
	if err != nil {
		return nil, err
	}
	rows, err := owner.queries.WithTx(transaction).ListCheckpoints(requestContext)
	if err != nil {
		_ = transaction.Rollback()
		return nil, err
	}
	if err := transaction.Commit(); err != nil {
		return nil, err
	}
	result := make(map[session.SessionID]projectioncache.CheckpointRecord, len(rows))
	for _, row := range rows {
		identifier, record, err := recordFromRow(row)
		if err != nil {
			return nil, err
		}
		if _, duplicate := result[identifier]; duplicate {
			return nil, fmt.Errorf(
				"session projection cache sqlite: duplicate Session %q",
				identifier,
			)
		}
		result[identifier] = record
	}
	return result, nil
}

// Replace atomically replaces one Session's complete checkpoint record.
func (owner *Adapter) Replace(
	requestContext context.Context,
	identifier session.SessionID,
	record projectioncache.CheckpointRecord,
) error {
	validated, err := projectioncache.ValidateCheckpointRecord(identifier, record)
	if err != nil {
		return fmt.Errorf("session projection cache sqlite: invalid record: %w", err)
	}
	rowsJSON, err := json.Marshal(validated.Rows)
	if err != nil {
		return fmt.Errorf("session projection cache sqlite: encode rows: %w", err)
	}
	return owner.queries.ReplaceCheckpoint(
		requestContext,
		dbsql.ReplaceCheckpointParams{
			SessionID: string(identifier),
			CreatedAt: validated.Identity.CreatedAt,
			Cwd:       nullableText(validated.Identity.CWD),
			RowsJson:  rowsJSON,
		},
	)
}

// Close idempotently releases the database connection.
func (owner *Adapter) Close(context.Context) error {
	owner.closeOnce.Do(func() {
		owner.closeErr = owner.database.Close()
	})
	return owner.closeErr
}

func recordFromRow(
	row dbsql.SessionProjectionCheckpoint,
) (session.SessionID, projectioncache.CheckpointRecord, error) {
	identifier := session.SessionID(row.SessionID)
	if identifier == "" {
		return "", projectioncache.CheckpointRecord{}, errors.New(
			"session projection cache sqlite: stored Session ID is empty",
		)
	}
	if !jsonvalue.IsObject(row.RowsJson) {
		return "", projectioncache.CheckpointRecord{}, fmt.Errorf(
			"session projection cache sqlite: Session %q rows are not one plain JSON object",
			identifier,
		)
	}
	decoder := json.NewDecoder(bytes.NewReader(row.RowsJson))
	decoder.DisallowUnknownFields()
	var rows sessionprojection.Checkpoint
	if err := decoder.Decode(&rows); err != nil {
		return "", projectioncache.CheckpointRecord{}, fmt.Errorf(
			"session projection cache sqlite: decode Session %q rows: %w",
			identifier,
			err,
		)
	}
	record := projectioncache.CheckpointRecord{
		Identity: projectioncache.LogIdentity{
			CreatedAt: row.CreatedAt,
			CWD:       textPointer(row.Cwd),
		},
		Rows: rows,
	}
	return identifier, record, nil
}

func nullableText(value *string) sql.NullString {
	if value == nil {
		return sql.NullString{}
	}
	return sql.NullString{
		String: *value,
		Valid:  true,
	}
}

func textPointer(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	text := value.String
	return &text
}

var _ projectioncache.CheckpointStore = (*Adapter)(nil)
