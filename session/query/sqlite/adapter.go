package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"sync"

	"github.com/gorenx/goren/session"
	sessionquery "github.com/gorenx/goren/session/query"
	"github.com/gorenx/goren/session/query/sqlite/internal/dbsql"
)

// Adapter stores only a disposable full-text read model. Source observation,
// live precedence, and revision decisions remain in session/query.Service.
type Adapter struct {
	database      *sql.DB
	queries       *dbsql.Queries
	path          string
	snippetLength int
	closeOnce     sync.Once
	closeErr      error
}

// Open validates index ownership and initializes its private schema.
func Open(requestContext context.Context, settings Config) (*Adapter, error) {
	resolved, err := settings.validate()
	if err != nil {
		return nil, err
	}
	database, actualPath, err := openDatabase(requestContext, resolved)
	if err != nil {
		return nil, err
	}
	return &Adapter{
		database: database, queries: dbsql.New(database), path: actualPath,
		snippetLength: resolved.SnippetCodePoints,
	}, nil
}

func (owner *Adapter) Snapshot(requestContext context.Context) (sessionquery.IndexSnapshot, error) {
	generation, err := owner.queries.GetIndexGeneration(requestContext)
	if err != nil {
		return sessionquery.IndexSnapshot{}, err
	}
	rows, err := owner.queries.ListIndexedSessions(requestContext)
	if err != nil {
		return sessionquery.IndexSnapshot{}, err
	}
	result := sessionquery.IndexSnapshot{
		Generation: generation,
		Sessions:   make(map[session.SessionID]sessionquery.IndexedSession, len(rows)),
	}
	for _, row := range rows {
		indexed, err := indexedSessionFromRow(row)
		if err != nil {
			return sessionquery.IndexSnapshot{}, fmt.Errorf("session query sqlite: invalid indexed Session %q: %w", row.ID, err)
		}
		if _, duplicate := result.Sessions[indexed.Header.ID]; duplicate {
			return sessionquery.IndexSnapshot{}, fmt.Errorf("session query sqlite: duplicate indexed Session %q", indexed.Header.ID)
		}
		result.Sessions[indexed.Header.ID] = indexed
	}
	return result, nil
}

func (owner *Adapter) Reconcile(
	requestContext context.Context,
	delta sessionquery.Reconciliation,
) (sessionquery.IndexSnapshot, error) {
	if len(delta.Replace) == 0 && len(delta.Remove) == 0 {
		return owner.Snapshot(requestContext)
	}
	transaction, err := owner.database.BeginTx(requestContext, nil)
	if err != nil {
		return sessionquery.IndexSnapshot{}, err
	}
	queries := owner.queries.WithTx(transaction)
	rollback := func() {
		_ = transaction.Rollback()
	}
	for _, identifier := range delta.Remove {
		if err := queries.DeleteIndexedDocuments(requestContext, string(identifier)); err != nil {
			rollback()
			return sessionquery.IndexSnapshot{}, err
		}
		if err := queries.DeleteIndexedSession(requestContext, string(identifier)); err != nil {
			rollback()
			return sessionquery.IndexSnapshot{}, err
		}
	}
	for _, replacement := range delta.Replace {
		generation := int64(1)
		current, rowErr := queries.GetIndexedSession(requestContext, string(replacement.Session.Header.ID))
		if rowErr == nil {
			generation = current.Generation + 1
		} else if !errors.Is(rowErr, sql.ErrNoRows) {
			rollback()
			return sessionquery.IndexSnapshot{}, rowErr
		}
		if replacement.ReplaceDocuments {
			if err := queries.DeleteIndexedDocuments(requestContext, string(replacement.Session.Header.ID)); err != nil {
				rollback()
				return sessionquery.IndexSnapshot{}, err
			}
			for _, entry := range replacement.Documents {
				if entry.SessionID != replacement.Session.Header.ID {
					rollback()
					return sessionquery.IndexSnapshot{}, fmt.Errorf(
						"session query sqlite: document Session %q does not match replacement %q",
						entry.SessionID, replacement.Session.Header.ID,
					)
				}
				if err := queries.InsertIndexedDocument(requestContext, dbsql.InsertIndexedDocumentParams{
					Text: sanitizeFTSText(entry.Text), SessionID: string(entry.SessionID),
					Seq: strconv.FormatInt(entry.Seq, 10), Type: entry.Type,
					Time: strconv.FormatInt(entry.Time, 10), Surface: string(entry.Surface),
				}); err != nil {
					rollback()
					return sessionquery.IndexSnapshot{}, err
				}
			}
		}
		parameters := indexedSessionParams(replacement.Session, generation)
		if err := queries.UpsertIndexedSession(requestContext, parameters); err != nil {
			rollback()
			return sessionquery.IndexSnapshot{}, err
		}
	}
	if err := queries.IncrementIndexGeneration(requestContext); err != nil {
		rollback()
		return sessionquery.IndexSnapshot{}, err
	}
	if err := transaction.Commit(); err != nil {
		return sessionquery.IndexSnapshot{}, err
	}
	return owner.Snapshot(requestContext)
}

func (owner *Adapter) SearchSessions(
	requestContext context.Context,
	criteria sessionquery.IndexedSearchRequest,
) ([]sessionquery.SessionHit, error) {
	statement, parameters, err := buildSessionSearch(criteria)
	if err != nil {
		return nil, err
	}
	rows, err := owner.database.QueryContext(requestContext, statement, parameters...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]sessionquery.SessionHit, 0)
	for rows.Next() {
		entry, err := scanSearchRow(rows, owner.snippetLength)
		if err != nil {
			return nil, err
		}
		result = append(result, sessionquery.SessionHit{
			SessionRecord: sessionquery.SessionRecord{
				Header: entry.header, Live: entry.live, Persisted: entry.persisted,
			},
			BestMatch: entry.hit,
		})
	}
	return result, rows.Err()
}

func (owner *Adapter) SearchEvents(
	requestContext context.Context,
	criteria sessionquery.IndexedEventSearchRequest,
) (session.Header, []sessionquery.EventHit, error) {
	statement, parameters, err := buildEventSearch(criteria)
	if err != nil {
		return session.Header{}, nil, err
	}
	rows, err := owner.database.QueryContext(requestContext, statement, parameters...)
	if err != nil {
		return session.Header{}, nil, err
	}
	defer rows.Close()
	var metadata session.Header
	result := make([]sessionquery.EventHit, 0)
	for rows.Next() {
		entry, err := scanSearchRow(rows, owner.snippetLength)
		if err != nil {
			return session.Header{}, nil, err
		}
		metadata = entry.header
		result = append(result, entry.hit)
	}
	if err := rows.Err(); err != nil {
		return session.Header{}, nil, err
	}
	if metadata.ID == "" {
		row, err := owner.queries.GetIndexedSession(requestContext, string(criteria.SessionID))
		if err != nil {
			return session.Header{}, nil, err
		}
		indexed, err := indexedSessionFromRow(row)
		if err != nil {
			return session.Header{}, nil, err
		}
		metadata = indexed.Header
	}
	return metadata, result, nil
}

// Close releases the one connection that owns the derived FTS index.
func (owner *Adapter) Close(context.Context) error {
	owner.closeOnce.Do(func() {
		owner.closeErr = owner.database.Close()
	})
	return owner.closeErr
}

func indexedSessionParams(source sessionquery.IndexedSession, generation int64) dbsql.UpsertIndexedSessionParams {
	metadata := source.Header
	return dbsql.UpsertIndexedSessionParams{
		ID: string(metadata.ID), Version: int64(metadata.Version), CreatedAt: metadata.CreatedAt,
		Cwd: nullableText(metadata.CWD), ParentSession: nullableSessionID(metadata.ParentSession),
		SeedLength: nullableInteger(metadata.SeedLength), Origin: string(metadata.Origin),
		DelegationDepth: nullableInteger(metadata.DelegationDepth), AgentPreset: nullableText(metadata.AgentPreset),
		Live: boolInteger(source.Live), Persisted: boolInteger(source.Persisted),
		SourceRevision: source.SourceRevision, Generation: generation,
	}
}

func indexedSessionFromRow(row dbsql.IndexedSession) (sessionquery.IndexedSession, error) {
	if row.Live != 0 && row.Live != 1 || row.Persisted != 0 && row.Persisted != 1 || row.Generation < 1 {
		return sessionquery.IndexedSession{}, errors.New("invalid availability or generation")
	}
	identifier := session.SessionID(row.ID)
	createdAt := row.CreatedAt
	metadata := session.Metadata{
		CreatedAt: &createdAt, CWD: textPointer(row.Cwd), ParentSession: sessionIDPointer(row.ParentSession),
		SeedLength: integerPointer(row.SeedLength), Origin: session.Origin(row.Origin),
		DelegationDepth: integerPointer(row.DelegationDepth), AgentPreset: textPointer(row.AgentPreset),
	}
	conversation, err := session.New(identifier, session.CreateOptions{Metadata: metadata})
	if err != nil {
		return sessionquery.IndexedSession{}, err
	}
	header := conversation.Header()
	if int64(header.Version) != row.Version {
		return sessionquery.IndexedSession{}, fmt.Errorf("header version %d is unsupported", row.Version)
	}
	return sessionquery.IndexedSession{
		Header: header, Live: row.Live == 1, Persisted: row.Persisted == 1,
		SourceRevision: row.SourceRevision, Generation: row.Generation,
	}, nil
}

func nullableText(value *string) sql.NullString {
	if value == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *value, Valid: true}
}

func nullableSessionID(value *session.SessionID) sql.NullString {
	if value == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: string(*value), Valid: true}
}

func nullableInteger(value *int64) sql.NullInt64 {
	if value == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *value, Valid: true}
}

func textPointer(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	result := value.String
	return &result
}

func sessionIDPointer(value sql.NullString) *session.SessionID {
	if !value.Valid {
		return nil
	}
	result := session.SessionID(value.String)
	return &result
}

func integerPointer(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	result := value.Int64
	return &result
}

func boolInteger(value bool) int64 {
	if value {
		return 1
	}
	return 0
}
