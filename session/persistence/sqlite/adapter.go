package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/gorenx/goren/session"
	sesspersist "github.com/gorenx/goren/session/persistence"
	"github.com/gorenx/goren/session/persistence/sqlite/internal/dbsql"
)

// Adapter implements only the storage primitives consumed by SessionLogStore.
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

func (owner *Adapter) Locate(session.Header) (sesspersist.Location, bool) {
	return sesspersist.Location{}, false
}

func (owner *Adapter) SupportsRawArtifacts() bool { return false }

func (owner *Adapter) ReadRaw(context.Context, session.SessionID) (*sesspersist.RawArtifact, error) {
	return nil, errors.New("session persistence sqlite: raw artifacts are unavailable")
}

func (owner *Adapter) Load(
	requestContext context.Context,
	identifier session.SessionID,
) (*sesspersist.Log, error) {
	transaction, err := owner.database.BeginTx(requestContext, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	queries := owner.queries.WithTx(transaction)
	row, err := queries.GetSession(requestContext, string(identifier))
	if errors.Is(err, sql.ErrNoRows) {
		_ = transaction.Rollback()
		return nil, nil
	}
	if err != nil {
		_ = transaction.Rollback()
		return nil, err
	}
	rows, err := queries.ListEvents(requestContext, string(identifier))
	if err != nil {
		_ = transaction.Rollback()
		return nil, err
	}
	if err = transaction.Commit(); err != nil {
		return nil, err
	}
	metadata, err := rowToHeader(row)
	if err != nil {
		return nil, err
	}
	entries, marker, err := scanEventRows(listEventRows(rows), 0)
	if err != nil {
		return nil, err
	}
	result := sesspersist.Log{
		Header:   metadata,
		Events:   entries,
		Revision: owner.revision(row.Incarnation, row.Revision),
	}
	if marker != nil {
		result.Marker = *marker
	}
	return &result, nil
}

func (owner *Adapter) Revision(
	requestContext context.Context,
	identifier session.SessionID,
) (*sesspersist.Revision, error) {
	row, err := owner.queries.GetSessionRevision(requestContext, string(identifier))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	storedRevision := owner.revision(row.Incarnation, row.Revision)
	return &storedRevision, nil
}

func (owner *Adapter) ReadEventsFrom(
	requestContext context.Context,
	identifier session.SessionID,
	continuation sesspersist.EventContinuation,
) (*sesspersist.EventSegment, error) {
	transaction, err := owner.database.BeginTx(requestContext, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	queries := owner.queries.WithTx(transaction)
	row, err := queries.GetSession(requestContext, string(identifier))
	if errors.Is(err, sql.ErrNoRows) {
		_ = transaction.Rollback()
		return nil, nil
	}
	if err != nil {
		_ = transaction.Rollback()
		return nil, err
	}
	rows, err := queries.ListEventsFrom(requestContext, dbsql.ListEventsFromParams{
		SessionID: string(identifier),
		Seq:       continuation.FromSeq,
		Limit:     continuation.Limit + 1,
	})
	if err != nil {
		_ = transaction.Rollback()
		return nil, err
	}
	if err := transaction.Commit(); err != nil {
		return nil, err
	}
	metadata, err := rowToHeader(row)
	if err != nil {
		return nil, err
	}
	hasMore := int64(len(rows)) > continuation.Limit
	if hasMore {
		rows = rows[:continuation.Limit]
	}
	entries, marker, err := scanEventRows(suffixEventRows(rows), continuation.FromSeq)
	if err != nil {
		return nil, err
	}
	if marker != nil {
		return nil, fmt.Errorf(
			"session persistence sqlite: invalid stored suffix at seq %d", marker.from,
		)
	}
	return &sesspersist.EventSegment{
		Header:   metadata,
		Revision: owner.revision(row.Incarnation, row.Revision),
		Events:   entries,
		HasMore:  hasMore,
	}, nil
}

func (owner *Adapter) ReadEventsBefore(
	requestContext context.Context,
	identifier session.SessionID,
	page sesspersist.EventPage,
) (*sesspersist.EventWindow, error) {
	transaction, err := owner.database.BeginTx(requestContext, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	queries := owner.queries.WithTx(transaction)
	row, err := queries.GetSession(requestContext, string(identifier))
	if errors.Is(err, sql.ErrNoRows) {
		_ = transaction.Rollback()
		return nil, nil
	}
	if err != nil {
		_ = transaction.Rollback()
		return nil, err
	}
	queryLimit := page.Limit + 1
	var eventRows []eventRow
	if page.BeforeSeq == nil {
		rows, queryErr := queries.ListLatestEvents(requestContext, dbsql.ListLatestEventsParams{
			SessionID: string(identifier),
			Limit:     queryLimit,
		})
		if queryErr != nil {
			_ = transaction.Rollback()
			return nil, queryErr
		}
		eventRows = latestEventRows(rows)
	} else {
		rows, queryErr := queries.ListEventsBefore(requestContext, dbsql.ListEventsBeforeParams{
			SessionID: string(identifier),
			Seq:       *page.BeforeSeq,
			Limit:     queryLimit,
		})
		if queryErr != nil {
			_ = transaction.Rollback()
			return nil, queryErr
		}
		eventRows = beforeEventRows(rows)
	}
	if err := transaction.Commit(); err != nil {
		return nil, err
	}
	metadata, err := rowToHeader(row)
	if err != nil {
		return nil, err
	}
	hasEarlier := int64(len(eventRows)) > page.Limit
	if hasEarlier {
		eventRows = eventRows[:page.Limit]
	}
	if len(eventRows) == 0 {
		return &sesspersist.EventWindow{
			Header: metadata,
			Events: []session.Event{},
		}, nil
	}
	ascendingRows := append([]eventRow(nil), eventRows...)
	slices.Reverse(ascendingRows)
	entries, marker, err := scanEventRows(ascendingRows, ascendingRows[0].seq)
	if err != nil {
		return nil, err
	}
	if marker != nil {
		return nil, fmt.Errorf(
			"session persistence sqlite: invalid stored event window at seq %d",
			marker.from,
		)
	}
	slices.Reverse(entries)
	return &sesspersist.EventWindow{
		Header:     metadata,
		Events:     entries,
		HasEarlier: hasEarlier,
	}, nil
}

func (owner *Adapter) AppendBatch(
	requestContext context.Context,
	batch sesspersist.EventBatch,
) error {
	transaction, err := owner.database.BeginTx(requestContext, nil)
	if err != nil {
		return err
	}
	queries := owner.queries.WithTx(transaction)
	if batch.Materialize {
		incarnation, err := randomUUID()
		if err != nil {
			_ = transaction.Rollback()
			return err
		}
		if err := queries.InsertSession(requestContext, sessionParams(batch.Header, incarnation)); err != nil {
			_ = transaction.Rollback()
			return err
		}
	}
	for _, entry := range batch.Events {
		parameters, err := eventParams(batch.Header.ID, entry)
		if err != nil {
			_ = transaction.Rollback()
			return err
		}
		if err := queries.InsertEvent(requestContext, parameters); err != nil {
			_ = transaction.Rollback()
			return err
		}
	}
	changed, err := queries.IncrementRevision(requestContext, string(batch.Header.ID))
	if err != nil || changed != 1 {
		_ = transaction.Rollback()
		if err != nil {
			return err
		}
		return fmt.Errorf("session persistence sqlite: session %q disappeared during append", batch.Header.ID)
	}
	return transaction.Commit()
}

func (owner *Adapter) CommitRepair(
	requestContext context.Context,
	repair sesspersist.LogRepair,
) error {
	transaction, err := owner.database.BeginTx(requestContext, nil)
	if err != nil {
		return err
	}
	queries := owner.queries.WithTx(transaction)
	mutated := len(repair.ClosingEvents) != 0
	if repair.Marker != nil {
		torn, ok := repair.Marker.(tornTail)
		if !ok {
			_ = transaction.Rollback()
			return errors.New("session persistence sqlite: repair marker belongs to another backend")
		}
		if err := queries.DeleteEventsFrom(requestContext, dbsql.DeleteEventsFromParams{
			SessionID: string(repair.Header.ID), Seq: torn.from,
		}); err != nil {
			_ = transaction.Rollback()
			return err
		}
		mutated = true
	}
	for _, entry := range repair.ClosingEvents {
		parameters, err := eventParams(repair.Header.ID, entry)
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
		changed, err := queries.IncrementRevision(requestContext, string(repair.Header.ID))
		if err != nil || changed != 1 {
			_ = transaction.Rollback()
			if err != nil {
				return err
			}
			return fmt.Errorf("session persistence sqlite: session %q disappeared during repair", repair.Header.ID)
		}
	}
	return transaction.Commit()
}

func (owner *Adapter) List(
	requestContext context.Context,
	page sesspersist.SessionPage,
) (sesspersist.SnapshotPage, error) {
	queryLimit := page.Limit + 1
	var rows []dbsql.Session
	var err error
	if page.Cursor == nil {
		rows, err = owner.queries.ListLatestSessions(requestContext, queryLimit)
	} else {
		rows, err = owner.queries.ListSessionsAfter(
			requestContext,
			dbsql.ListSessionsAfterParams{
				CursorCreatedAt: page.Cursor.CreatedAt,
				CursorID:        string(page.Cursor.ID),
				QueryLimit:      queryLimit,
			},
		)
	}
	if err != nil {
		return sesspersist.SnapshotPage{}, err
	}
	hasMore := int64(len(rows)) > page.Limit
	if hasMore {
		rows = rows[:page.Limit]
	}
	result := make([]sesspersist.Snapshot, 0, len(rows))
	for _, row := range rows {
		metadata, err := rowToHeader(row)
		if err != nil {
			return sesspersist.SnapshotPage{}, err
		}
		result = append(result, sesspersist.Snapshot{
			Header:   metadata,
			Revision: owner.revision(row.Incarnation, row.Revision),
		})
	}
	pageResult := sesspersist.SnapshotPage{
		Snapshots: result,
	}
	if hasMore {
		last := result[len(result)-1].Header
		pageResult.NextCursor = &sesspersist.SessionCursor{
			CreatedAt: last.CreatedAt,
			ID:        last.ID,
		}
	}
	return pageResult, nil
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

func (owner *Adapter) revision(incarnation string, counter int64) sesspersist.Revision {
	return sesspersist.Revision(fmt.Sprintf(
		"%s:incarnation:%s:revision:%d", owner.storeIdentity, incarnation, counter,
	))
}
