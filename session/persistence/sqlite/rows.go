package sqlite

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/session/persistence/sqlite/internal/dbsql"
)

type eventRow struct {
	seq             int64
	typeName        string
	timeValue       int64
	data            string
	sourceEventSeqs sql.NullString
	surfaceOp       sql.NullString
	ignorable       sql.NullInt64
}

type tornTail struct {
	from int64
}

func (tornTail) PersistenceRepairMarker() {}

func rowToHeader(row dbsql.Session) (session.Header, error) {
	if row.Version < 0 || row.Version > int64(^uint(0)>>1) {
		return session.Header{}, errors.New("session persistence sqlite: stored format version is out of range")
	}
	metadata := session.Header{
		Version: int(row.Version), ID: session.SessionID(row.ID), CreatedAt: row.CreatedAt,
		CWD: nullableText(row.Cwd), ParentSession: nullableSessionID(row.ParentSession),
		SeedLength: nullableInt64(row.SeedLength), Origin: session.Origin(nullableTextValue(row.Origin)),
		DelegationDepth: nullableInt64(row.DelegationDepth), AgentPreset: nullableText(row.AgentPreset),
	}
	return metadata, nil
}

func sessionParams(metadata session.Header, incarnation string) dbsql.InsertSessionParams {
	return dbsql.InsertSessionParams{
		ID: string(metadata.ID), Version: int64(metadata.Version), CreatedAt: metadata.CreatedAt,
		Cwd: optionalText(metadata.CWD), ParentSession: optionalSessionID(metadata.ParentSession),
		SeedLength: optionalInt64(metadata.SeedLength), Origin: optionalOrigin(metadata.Origin),
		DelegationDepth: optionalInt64(metadata.DelegationDepth), AgentPreset: optionalText(metadata.AgentPreset),
		Incarnation: incarnation,
	}
}

func eventParams(identifier session.SessionID, entry session.Event) (dbsql.InsertEventParams, error) {
	if !json.Valid(entry.Data) {
		return dbsql.InsertEventParams{}, fmt.Errorf("session persistence sqlite: event %q data is invalid JSON", entry.Type)
	}
	parameters := dbsql.InsertEventParams{
		SessionID: string(identifier), Seq: entry.Seq, Type: entry.Type, Time: entry.Time,
		Data: string(entry.Data),
	}
	if entry.SourceEventSeqs != nil {
		encoded, err := json.Marshal(*entry.SourceEventSeqs)
		if err != nil {
			return dbsql.InsertEventParams{}, err
		}
		parameters.SourceEventSeqs = sql.NullString{String: string(encoded), Valid: true}
	}
	if entry.SurfaceOp != nil {
		encoded, err := json.Marshal(entry.SurfaceOp)
		if err != nil {
			return dbsql.InsertEventParams{}, err
		}
		parameters.SurfaceOp = sql.NullString{String: string(encoded), Valid: true}
	}
	if entry.Ignorable {
		parameters.Ignorable = sql.NullInt64{Int64: 1, Valid: true}
	}
	return parameters, nil
}

func scanEventRows(rows []eventRow, base int64) ([]session.Event, *tornTail, error) {
	parsed := make([]*session.Event, len(rows))
	lastTurnEnd := -1
	for index, row := range rows {
		entry, err := rowToEvent(row)
		if err == nil {
			parsed[index] = &entry
			if row.typeName == session.TurnEndEventName {
				lastTurnEnd = index
			}
		}
	}
	preserved := make([]session.Event, 0, len(rows))
	for index, entry := range parsed {
		if entry == nil {
			if index <= lastTurnEnd {
				return nil, nil, fmt.Errorf(
					"session persistence sqlite: unparsable committed event at seq %d", rows[index].seq,
				)
			}
			marker := &tornTail{from: base + int64(len(preserved))}
			return preserved, marker, nil
		}
		expected := base + int64(index)
		if entry.Seq != expected {
			if index <= lastTurnEnd {
				return nil, nil, fmt.Errorf(
					"session persistence sqlite: seq gap in committed region: expected %d, got %d",
					expected, entry.Seq,
				)
			}
			marker := &tornTail{from: base + int64(len(preserved))}
			return preserved, marker, nil
		}
		preserved = append(preserved, *entry)
	}
	return preserved, nil, nil
}

func rowToEvent(row eventRow) (session.Event, error) {
	rawData := json.RawMessage(row.data)
	if !json.Valid(rawData) {
		return session.Event{}, errors.New("invalid event data JSON")
	}
	entry := session.Event{
		Type: row.typeName, Seq: row.seq, Time: row.timeValue,
		Data: append(json.RawMessage(nil), rawData...),
	}
	if row.sourceEventSeqs.Valid {
		trimmed := bytes.TrimSpace([]byte(row.sourceEventSeqs.String))
		if len(trimmed) < 2 || trimmed[0] != '[' {
			return session.Event{}, errors.New("invalid source event seqs JSON")
		}
		var sequences []int64
		if err := json.Unmarshal(trimmed, &sequences); err != nil {
			return session.Event{}, err
		}
		entry.SourceEventSeqs = &sequences
	}
	if row.surfaceOp.Valid {
		var operation session.SurfaceOperation
		if err := json.Unmarshal([]byte(row.surfaceOp.String), &operation); err != nil {
			return session.Event{}, err
		}
		entry.SurfaceOp = &operation
	}
	if row.ignorable.Valid {
		if row.ignorable.Int64 != 0 && row.ignorable.Int64 != 1 {
			return session.Event{}, errors.New("invalid ignorable marker")
		}
		entry.Ignorable = row.ignorable.Int64 == 1
	}
	return entry, nil
}

func listEventRows(rows []dbsql.ListEventsRow) []eventRow {
	result := make([]eventRow, len(rows))
	for index, row := range rows {
		result[index] = eventRow{
			seq: row.Seq, typeName: row.Type, timeValue: row.Time, data: row.Data,
			sourceEventSeqs: row.SourceEventSeqs, surfaceOp: row.SurfaceOp, ignorable: row.Ignorable,
		}
	}
	return result
}

func suffixEventRows(rows []dbsql.ListEventsFromRow) []eventRow {
	result := make([]eventRow, len(rows))
	for index, row := range rows {
		result[index] = eventRow{
			seq: row.Seq, typeName: row.Type, timeValue: row.Time, data: row.Data,
			sourceEventSeqs: row.SourceEventSeqs, surfaceOp: row.SurfaceOp, ignorable: row.Ignorable,
		}
	}
	return result
}

func nullableText(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	copyValue := value.String
	return &copyValue
}

func nullableTextValue(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

func nullableSessionID(value sql.NullString) *session.SessionID {
	if !value.Valid {
		return nil
	}
	copyValue := session.SessionID(value.String)
	return &copyValue
}

func nullableInt64(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	copyValue := value.Int64
	return &copyValue
}

func optionalText(value *string) sql.NullString {
	if value == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *value, Valid: true}
}

func optionalSessionID(value *session.SessionID) sql.NullString {
	if value == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: string(*value), Valid: true}
}

func optionalInt64(value *int64) sql.NullInt64 {
	if value == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *value, Valid: true}
}

func optionalOrigin(value session.Origin) sql.NullString {
	if value == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: string(value), Valid: true}
}
