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
	boundcontract "github.com/gorenx/goren/subagent/bound"
	"github.com/gorenx/goren/subagent/internal/bound"
	"github.com/gorenx/goren/subagent/internal/bound/sqlite/internal/dbsql"
)

// Adapter maps complete Bound Definitions to one independently owned SQLite
// database. It implements no revision policy or live Session reconciliation.
type Adapter struct {
	database  *sql.DB
	queries   *dbsql.Queries
	closeOnce sync.Once
	closeErr  error
}

// Open validates database ownership and initializes the Definition schema.
func Open(
	requestContext context.Context,
	settings Config,
) (*Adapter, error) {
	database, err := openDatabase(requestContext, settings)
	if err != nil {
		return nil, err
	}
	return &Adapter{
		database: database,
		queries:  dbsql.New(database),
	}, nil
}

// Load returns one transactionally consistent Definition index.
func (owner *Adapter) Load(
	requestContext context.Context,
) ([]boundcontract.Definition, error) {
	transaction, err := owner.database.BeginTx(
		requestContext,
		&sql.TxOptions{
			ReadOnly: true,
		},
	)
	if err != nil {
		return nil, err
	}
	rows, err := owner.queries.WithTx(transaction).ListDefinitions(
		requestContext,
	)
	if err != nil {
		_ = transaction.Rollback()
		return nil, err
	}
	if err = transaction.Commit(); err != nil {
		return nil, err
	}
	definitions := make([]boundcontract.Definition, 0, len(rows))
	for _, row := range rows {
		definitionValue, decodeErr := definitionFromRow(row)
		if decodeErr != nil {
			return nil, decodeErr
		}
		definitions = append(definitions, definitionValue)
	}
	return definitions, nil
}

// Create inserts the first complete revision for one stable name.
func (owner *Adapter) Create(
	requestContext context.Context,
	definitionValue boundcontract.Definition,
) error {
	validated, encoded, err := encodeDefinition(definitionValue)
	if err != nil {
		return err
	}
	if validated.Revision != 1 {
		return errors.New(
			"subagent Bound Definition sqlite: Create requires revision 1",
		)
	}
	changed, err := owner.queries.InsertDefinition(
		requestContext,
		dbsql.InsertDefinitionParams{
			Name:           validated.Name,
			Revision:       validated.Revision,
			DefinitionJson: encoded,
		},
	)
	if err != nil {
		return err
	}
	switch changed {
	case 1:
		return nil
	case 0:
		return bound.ErrDefinitionExists
	default:
		return fmt.Errorf(
			"subagent Bound Definition sqlite: Create changed %d rows",
			changed,
		)
	}
}

// Replace conditionally commits one complete next revision.
func (owner *Adapter) Replace(
	requestContext context.Context,
	expectedRevision int64,
	definitionValue boundcontract.Definition,
) error {
	validated, encoded, err := encodeDefinition(definitionValue)
	if err != nil {
		return err
	}
	if expectedRevision <= 0 || validated.Revision != expectedRevision+1 {
		return errors.New(
			"subagent Bound Definition sqlite: Replace revision chain is invalid",
		)
	}
	transaction, err := owner.database.BeginTx(requestContext, nil)
	if err != nil {
		return err
	}
	queries := owner.queries.WithTx(transaction)
	changed, err := queries.ReplaceDefinition(
		requestContext,
		dbsql.ReplaceDefinitionParams{
			Revision:       validated.Revision,
			DefinitionJson: encoded,
			Name:           validated.Name,
			Revision_2:     expectedRevision,
		},
	)
	if err != nil {
		_ = transaction.Rollback()
		return err
	}
	if changed == 1 {
		return transaction.Commit()
	}
	if changed != 0 {
		_ = transaction.Rollback()
		return fmt.Errorf(
			"subagent Bound Definition sqlite: Replace changed %d rows",
			changed,
		)
	}
	_, lookupErr := queries.DefinitionRevision(
		requestContext,
		validated.Name,
	)
	_ = transaction.Rollback()
	if errors.Is(lookupErr, sql.ErrNoRows) {
		return bound.ErrDefinitionNotFound
	}
	if lookupErr != nil {
		return lookupErr
	}
	return bound.ErrDefinitionRevisionConflict
}

// Close idempotently releases the database connection.
func (owner *Adapter) Close(closeContext context.Context) error {
	if closeContext != nil {
		select {
		case <-closeContext.Done():
			return context.Cause(closeContext)
		default:
		}
	}
	owner.closeOnce.Do(func() {
		owner.closeErr = owner.database.Close()
	})
	return owner.closeErr
}

func definitionFromRow(
	row dbsql.BoundDefinition,
) (boundcontract.Definition, error) {
	if !jsonvalue.IsObject(row.DefinitionJson) {
		return boundcontract.Definition{}, fmt.Errorf(
			"subagent Bound Definition sqlite: Definition %q is not a JSON object",
			row.Name,
		)
	}
	decoder := json.NewDecoder(bytes.NewReader(row.DefinitionJson))
	decoder.DisallowUnknownFields()
	var definitionValue boundcontract.Definition
	if err := decoder.Decode(&definitionValue); err != nil {
		return boundcontract.Definition{}, fmt.Errorf(
			"subagent Bound Definition sqlite: decode Definition %q: %w",
			row.Name,
			err,
		)
	}
	if definitionValue.Name != row.Name ||
		definitionValue.Revision != row.Revision {
		return boundcontract.Definition{}, fmt.Errorf(
			"subagent Bound Definition sqlite: row identity for %q is inconsistent",
			row.Name,
		)
	}
	return definitionValue, nil
}

func encodeDefinition(
	definitionValue boundcontract.Definition,
) (boundcontract.Definition, []byte, error) {
	validated, err := boundcontract.SnapshotDefinition(definitionValue)
	if err != nil {
		return boundcontract.Definition{}, nil, fmt.Errorf(
			"subagent Bound Definition sqlite: invalid Definition: %w",
			err,
		)
	}
	encoded, err := json.Marshal(validated)
	if err != nil {
		return boundcontract.Definition{}, nil, err
	}
	return validated, encoded, nil
}

var _ bound.DefinitionStore = (*Adapter)(nil)
