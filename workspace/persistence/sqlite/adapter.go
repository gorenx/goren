package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/workspace"
	"github.com/gorenx/goren/workspace/persistence/sqlite/internal/dbsql"
)

// Adapter maps Workspace-owned records to SQLite rows. It implements no
// filesystem, membership, ordering, or archive policy.
type Adapter struct {
	database  *sql.DB
	queries   *dbsql.Queries
	closeOnce sync.Once
	closeErr  error
}

// Open validates schema ownership before returning a Workspace Backend.
func Open(requestContext context.Context, settings Config) (*Adapter, error) {
	database, err := openDatabase(requestContext, settings)
	if err != nil {
		return nil, err
	}
	return &Adapter{database: database, queries: dbsql.New(database)}, nil
}

func (owner *Adapter) Load(requestContext context.Context) (workspace.StoredRegistry, error) {
	transaction, err := owner.database.BeginTx(requestContext, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return workspace.StoredRegistry{}, err
	}
	queries := owner.queries.WithTx(transaction)
	stateRow, err := queries.GetWorkspaceState(requestContext)
	if err != nil {
		_ = transaction.Rollback()
		return workspace.StoredRegistry{}, err
	}
	rows, err := queries.ListWorkspaces(requestContext)
	if err != nil {
		_ = transaction.Rollback()
		return workspace.StoredRegistry{}, err
	}
	if err := transaction.Commit(); err != nil {
		return workspace.StoredRegistry{}, err
	}
	workspaceIDs, err := decodeWorkspaceIDs(stateRow.WorkspaceIds)
	if err != nil {
		return workspace.StoredRegistry{}, fmt.Errorf("workspace persistence sqlite: decode order: %w", err)
	}
	archivedIDs, err := decodeSessionIDs(stateRow.ArchivedSessionIds)
	if err != nil {
		return workspace.StoredRegistry{}, fmt.Errorf("workspace persistence sqlite: decode archive set: %w", err)
	}
	records := make([]workspace.StoredWorkspace, 0, len(rows))
	for _, row := range rows {
		record, err := decodeRecord(row)
		if err != nil {
			return workspace.StoredRegistry{}, err
		}
		records = append(records, record)
	}
	return workspace.StoredRegistry{
		Initialized: stateRow.Initialized == 1, WorkspaceIDs: workspaceIDs,
		ArchivedSessionIDs: archivedIDs, Records: records,
	}, nil
}

func (owner *Adapter) Initialize(requestContext context.Context, stored workspace.StoredRegistry) error {
	transaction, err := owner.database.BeginTx(requestContext, nil)
	if err != nil {
		return err
	}
	queries := owner.queries.WithTx(transaction)
	if err := queries.DeleteAllWorkspaces(requestContext); err != nil {
		_ = transaction.Rollback()
		return err
	}
	for _, record := range stored.Records {
		parameters, err := insertParams(record)
		if err != nil {
			_ = transaction.Rollback()
			return err
		}
		if err := queries.InsertWorkspace(requestContext, parameters); err != nil {
			_ = transaction.Rollback()
			return err
		}
	}
	stateParameters, err := stateParams(stored.Initialized, stored.WorkspaceIDs, stored.ArchivedSessionIDs)
	if err != nil {
		_ = transaction.Rollback()
		return err
	}
	if err := queries.PutWorkspaceState(requestContext, stateParameters); err != nil {
		_ = transaction.Rollback()
		return err
	}
	return transaction.Commit()
}

func (owner *Adapter) Create(
	requestContext context.Context,
	record workspace.StoredWorkspace,
	workspaceIDs []workspace.ID,
) error {
	transaction, err := owner.database.BeginTx(requestContext, nil)
	if err != nil {
		return err
	}
	queries := owner.queries.WithTx(transaction)
	parameters, err := insertParams(record)
	if err != nil {
		_ = transaction.Rollback()
		return err
	}
	if err := queries.InsertWorkspace(requestContext, parameters); err != nil {
		_ = transaction.Rollback()
		return err
	}
	encodedOrder, err := encodeWorkspaceIDs(workspaceIDs)
	if err != nil {
		_ = transaction.Rollback()
		return err
	}
	changed, err := queries.SetWorkspaceOrder(requestContext, encodedOrder)
	if err != nil || changed != 1 {
		_ = transaction.Rollback()
		if err != nil {
			return err
		}
		return errors.New("workspace persistence sqlite: registry state disappeared during create")
	}
	return transaction.Commit()
}

func (owner *Adapter) Update(requestContext context.Context, record workspace.StoredWorkspace) error {
	parameters, err := updateParams(record)
	if err != nil {
		return err
	}
	changed, err := owner.queries.UpdateWorkspace(requestContext, parameters)
	if err != nil {
		return err
	}
	if changed != 1 {
		return fmt.Errorf("workspace persistence sqlite: workspace %q disappeared during update", record.ID)
	}
	return nil
}

func (owner *Adapter) Delete(
	requestContext context.Context,
	identifier workspace.ID,
	workspaceIDs []workspace.ID,
) error {
	transaction, err := owner.database.BeginTx(requestContext, nil)
	if err != nil {
		return err
	}
	queries := owner.queries.WithTx(transaction)
	changed, err := queries.DeleteWorkspace(requestContext, string(identifier))
	if err != nil || changed != 1 {
		_ = transaction.Rollback()
		if err != nil {
			return err
		}
		return fmt.Errorf("workspace persistence sqlite: workspace %q disappeared during delete", identifier)
	}
	encodedOrder, err := encodeWorkspaceIDs(workspaceIDs)
	if err != nil {
		_ = transaction.Rollback()
		return err
	}
	changed, err = queries.SetWorkspaceOrder(requestContext, encodedOrder)
	if err != nil || changed != 1 {
		_ = transaction.Rollback()
		if err != nil {
			return err
		}
		return errors.New("workspace persistence sqlite: registry state disappeared during delete")
	}
	return transaction.Commit()
}

func (owner *Adapter) SetOrder(requestContext context.Context, workspaceIDs []workspace.ID) error {
	encoded, err := encodeWorkspaceIDs(workspaceIDs)
	if err != nil {
		return err
	}
	changed, err := owner.queries.SetWorkspaceOrder(requestContext, encoded)
	if err != nil {
		return err
	}
	if changed != 1 {
		return errors.New("workspace persistence sqlite: registry state disappeared during reorder")
	}
	return nil
}

func (owner *Adapter) SetArchivedSessionIDs(
	requestContext context.Context,
	identifiers []session.SessionID,
) error {
	encoded, err := encodeSessionIDs(identifiers)
	if err != nil {
		return err
	}
	changed, err := owner.queries.SetArchivedSessionIDs(requestContext, encoded)
	if err != nil {
		return err
	}
	if changed != 1 {
		return errors.New("workspace persistence sqlite: registry state disappeared during archive")
	}
	return nil
}

func (owner *Adapter) Close(closeContext context.Context) error {
	if closeContext != nil {
		select {
		case <-closeContext.Done():
			return context.Cause(closeContext)
		default:
		}
	}
	owner.closeOnce.Do(func() { owner.closeErr = owner.database.Close() })
	return owner.closeErr
}

func insertParams(record workspace.StoredWorkspace) (dbsql.InsertWorkspaceParams, error) {
	sessionIDs, err := encodeSessionIDs(record.SessionIDs)
	if err != nil {
		return dbsql.InsertWorkspaceParams{}, err
	}
	return dbsql.InsertWorkspaceParams{
		ID: string(record.ID), Path: record.Path, Title: record.Title, SessionIds: sessionIDs,
		CreatedAt: record.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt: record.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}, nil
}

func updateParams(record workspace.StoredWorkspace) (dbsql.UpdateWorkspaceParams, error) {
	insert, err := insertParams(record)
	if err != nil {
		return dbsql.UpdateWorkspaceParams{}, err
	}
	return dbsql.UpdateWorkspaceParams{
		ID: insert.ID, Path: insert.Path, Title: insert.Title, SessionIds: insert.SessionIds,
		CreatedAt: insert.CreatedAt, UpdatedAt: insert.UpdatedAt,
	}, nil
}

func stateParams(
	initialized bool,
	workspaceIDs []workspace.ID,
	archivedSessionIDs []session.SessionID,
) (dbsql.PutWorkspaceStateParams, error) {
	encodedOrder, err := encodeWorkspaceIDs(workspaceIDs)
	if err != nil {
		return dbsql.PutWorkspaceStateParams{}, err
	}
	encodedArchive, err := encodeSessionIDs(archivedSessionIDs)
	if err != nil {
		return dbsql.PutWorkspaceStateParams{}, err
	}
	initializedValue := int64(0)
	if initialized {
		initializedValue = 1
	}
	return dbsql.PutWorkspaceStateParams{
		Initialized: initializedValue, WorkspaceIds: encodedOrder, ArchivedSessionIds: encodedArchive,
	}, nil
}

func decodeRecord(row dbsql.Workspace) (workspace.StoredWorkspace, error) {
	sessionIDs, err := decodeSessionIDs(row.SessionIds)
	if err != nil {
		return workspace.StoredWorkspace{}, fmt.Errorf("workspace persistence sqlite: decode workspace %q sessions: %w", row.ID, err)
	}
	createdAt, err := time.Parse(time.RFC3339Nano, row.CreatedAt)
	if err != nil {
		return workspace.StoredWorkspace{}, fmt.Errorf("workspace persistence sqlite: decode workspace %q createdAt: %w", row.ID, err)
	}
	updatedAt, err := time.Parse(time.RFC3339Nano, row.UpdatedAt)
	if err != nil {
		return workspace.StoredWorkspace{}, fmt.Errorf("workspace persistence sqlite: decode workspace %q updatedAt: %w", row.ID, err)
	}
	return workspace.StoredWorkspace{
		ID: workspace.ID(row.ID), Path: row.Path, Title: row.Title, SessionIDs: sessionIDs,
		CreatedAt: createdAt, UpdatedAt: updatedAt,
	}, nil
}

func encodeWorkspaceIDs(identifiers []workspace.ID) (string, error) {
	values := make([]string, len(identifiers))
	for index, identifier := range identifiers {
		values[index] = string(identifier)
	}
	encoded, err := json.Marshal(values)
	return string(encoded), err
}

func decodeWorkspaceIDs(encoded string) ([]workspace.ID, error) {
	var values []string
	if err := json.Unmarshal([]byte(encoded), &values); err != nil || values == nil {
		if err == nil {
			err = errors.New("value is not an array")
		}
		return nil, err
	}
	result := make([]workspace.ID, len(values))
	for index, value := range values {
		result[index] = workspace.ID(value)
	}
	return result, nil
}

func encodeSessionIDs(identifiers []session.SessionID) (string, error) {
	values := make([]string, len(identifiers))
	for index, identifier := range identifiers {
		values[index] = string(identifier)
	}
	encoded, err := json.Marshal(values)
	return string(encoded), err
}

func decodeSessionIDs(encoded string) ([]session.SessionID, error) {
	var values []string
	if err := json.Unmarshal([]byte(encoded), &values); err != nil || values == nil {
		if err == nil {
			err = errors.New("value is not an array")
		}
		return nil, err
	}
	result := make([]session.SessionID, len(values))
	for index, value := range values {
		result[index] = session.SessionID(value)
	}
	return result, nil
}
