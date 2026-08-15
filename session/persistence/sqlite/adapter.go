package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"

	"github.com/gorenx/goren/session"
	sessionpersistence "github.com/gorenx/goren/session/persistence"
	"github.com/gorenx/goren/session/persistence/sqlite/internal/dbsql"
)

// Adapter implements only the storage primitives consumed by Persistence Coordinator.
type Adapter struct {
	database      *sql.DB
	queries       *dbsql.Queries
	storeIdentity string
	closeOnce     sync.Once
	closeErr      error
}

// Open validates ownership and schema before returning a storage adapter.
func Open(requestContext context.Context, settings Config) (*Adapter, error) {
	database, storeIdentity, err := openDatabase(requestContext, settings)
	if err != nil {
		return nil, err
	}
	return &Adapter{database: database, queries: dbsql.New(database), storeIdentity: storeIdentity}, nil
}

func (owner *Adapter) BackendName() string { return "session-persistence-sqlite" }

func (owner *Adapter) Locate(session.Header) (sessionpersistence.Location, bool) {
	return sessionpersistence.Location{}, false
}

func (owner *Adapter) SupportsRawArtifacts() bool { return false }

func (owner *Adapter) ReadRaw(context.Context, session.SessionID) (sessionpersistence.RawArtifact, bool, error) {
	return sessionpersistence.RawArtifact{}, false, errors.New("session persistence sqlite: raw artifacts are unavailable")
}

func (owner *Adapter) LoadStored(
	requestContext context.Context,
	identifier session.SessionID,
) (sessionpersistence.StoredPrefix, bool, error) {
	transaction, err := owner.database.BeginTx(requestContext, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return sessionpersistence.StoredPrefix{}, false, err
	}
	queries := owner.queries.WithTx(transaction)
	row, err := queries.GetSession(requestContext, string(identifier))
	if errors.Is(err, sql.ErrNoRows) {
		_ = transaction.Rollback()
		return sessionpersistence.StoredPrefix{}, false, nil
	}
	if err != nil {
		_ = transaction.Rollback()
		return sessionpersistence.StoredPrefix{}, false, err
	}
	rows, err := queries.ListEvents(requestContext, string(identifier))
	if err != nil {
		_ = transaction.Rollback()
		return sessionpersistence.StoredPrefix{}, false, err
	}
	if err := transaction.Commit(); err != nil {
		return sessionpersistence.StoredPrefix{}, false, err
	}
	metadata, err := rowToHeader(row)
	if err != nil {
		return sessionpersistence.StoredPrefix{}, false, err
	}
	entries, marker, err := scanEventRows(listEventRows(rows), 0)
	if err != nil {
		return sessionpersistence.StoredPrefix{}, false, err
	}
	result := sessionpersistence.StoredPrefix{
		Header: metadata, Events: entries, Token: owner.revision(row.Incarnation, row.Revision),
	}
	if marker != nil {
		result.Marker = *marker
	}
	return result, true, nil
}

func (owner *Adapter) ReadStoredRevision(
	requestContext context.Context,
	identifier session.SessionID,
) (sessionpersistence.Revision, bool, error) {
	row, err := owner.queries.GetSessionRevision(requestContext, string(identifier))
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return owner.revision(row.Incarnation, row.Revision), true, nil
}

func (owner *Adapter) LoadStoredFrom(
	requestContext context.Context,
	identifier session.SessionID,
	fromSeq int64,
) (sessionpersistence.StoredSuffix, bool, error) {
	row, err := owner.queries.GetSession(requestContext, string(identifier))
	if errors.Is(err, sql.ErrNoRows) {
		return sessionpersistence.StoredSuffix{}, false, nil
	}
	if err != nil {
		return sessionpersistence.StoredSuffix{}, false, err
	}
	rows, err := owner.queries.ListEventsFrom(requestContext, dbsql.ListEventsFromParams{
		SessionID: string(identifier), Seq: fromSeq,
	})
	if err != nil {
		return sessionpersistence.StoredSuffix{}, false, err
	}
	metadata, err := rowToHeader(row)
	if err != nil {
		return sessionpersistence.StoredSuffix{}, false, err
	}
	entries, _, err := scanEventRows(suffixEventRows(rows), fromSeq)
	if err != nil {
		return sessionpersistence.StoredSuffix{}, false, err
	}
	return sessionpersistence.StoredSuffix{Header: metadata, Events: entries}, true, nil
}

func (owner *Adapter) AppendBatch(
	requestContext context.Context,
	metadata session.Header,
	entries []session.Event,
	alreadyMaterialized bool,
) error {
	transaction, err := owner.database.BeginTx(requestContext, nil)
	if err != nil {
		return err
	}
	queries := owner.queries.WithTx(transaction)
	if !alreadyMaterialized {
		incarnation, err := randomUUID()
		if err != nil {
			_ = transaction.Rollback()
			return err
		}
		if err := queries.InsertSession(requestContext, sessionParams(metadata, incarnation)); err != nil {
			_ = transaction.Rollback()
			return err
		}
	}
	for _, entry := range entries {
		parameters, err := eventParams(metadata.ID, entry)
		if err != nil {
			_ = transaction.Rollback()
			return err
		}
		if err := queries.InsertEvent(requestContext, parameters); err != nil {
			_ = transaction.Rollback()
			return err
		}
	}
	changed, err := queries.IncrementRevision(requestContext, string(metadata.ID))
	if err != nil || changed != 1 {
		_ = transaction.Rollback()
		if err != nil {
			return err
		}
		return fmt.Errorf("session persistence sqlite: session %q disappeared during append", metadata.ID)
	}
	return transaction.Commit()
}

func (owner *Adapter) CommitRepair(
	requestContext context.Context,
	metadata session.Header,
	marker sessionpersistence.RepairMarker,
	closers []session.Event,
) error {
	transaction, err := owner.database.BeginTx(requestContext, nil)
	if err != nil {
		return err
	}
	queries := owner.queries.WithTx(transaction)
	mutated := len(closers) != 0
	if marker != nil {
		torn, ok := marker.(tornTail)
		if !ok {
			_ = transaction.Rollback()
			return errors.New("session persistence sqlite: repair marker belongs to another backend")
		}
		if err := queries.DeleteEventsFrom(requestContext, dbsql.DeleteEventsFromParams{
			SessionID: string(metadata.ID), Seq: torn.from,
		}); err != nil {
			_ = transaction.Rollback()
			return err
		}
		mutated = true
	}
	for _, entry := range closers {
		parameters, err := eventParams(metadata.ID, entry)
		if err != nil {
			_ = transaction.Rollback()
			return err
		}
		if err := queries.InsertEvent(requestContext, parameters); err != nil {
			_ = transaction.Rollback()
			return err
		}
	}
	if mutated {
		changed, err := queries.IncrementRevision(requestContext, string(metadata.ID))
		if err != nil || changed != 1 {
			_ = transaction.Rollback()
			if err != nil {
				return err
			}
			return fmt.Errorf("session persistence sqlite: session %q disappeared during repair", metadata.ID)
		}
	}
	return transaction.Commit()
}

func (owner *Adapter) ListStored(requestContext context.Context) ([]session.Header, error) {
	rows, err := owner.queries.ListSessions(requestContext)
	if err != nil {
		return nil, err
	}
	result := make([]session.Header, 0, len(rows))
	for _, row := range rows {
		metadata, err := rowToHeader(row)
		if err != nil {
			return nil, err
		}
		result = append(result, metadata)
	}
	return result, nil
}

func (owner *Adapter) ListStoredSnapshots(requestContext context.Context) ([]sessionpersistence.Snapshot, error) {
	rows, err := owner.queries.ListSessions(requestContext)
	if err != nil {
		return nil, err
	}
	result := make([]sessionpersistence.Snapshot, 0, len(rows))
	for _, row := range rows {
		metadata, err := rowToHeader(row)
		if err != nil {
			return nil, err
		}
		result = append(result, sessionpersistence.Snapshot{
			Header: metadata, Revision: owner.revision(row.Incarnation, row.Revision),
		})
	}
	return result, nil
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

func (owner *Adapter) revision(incarnation string, counter int64) sessionpersistence.Revision {
	return sessionpersistence.Revision(fmt.Sprintf(
		"%s:incarnation:%s:revision:%d", owner.storeIdentity, incarnation, counter,
	))
}
