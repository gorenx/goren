package sqlite

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/gorenx/goren/session"
	sessionquery "github.com/gorenx/goren/session/query"
	"github.com/gorenx/goren/session/query/sqlite/internal/dbsql"
)

const (
	highlightStart = "\uFDD0"
	highlightEnd   = "\uFDD1"
	variableLimit  = 32_766
)

type searchRow struct {
	header    session.Header
	live      bool
	persisted bool
	hit       sessionquery.EventHit
}

type rowScanner interface {
	Scan(...any) error
}

func buildSessionSearch(criteria sessionquery.IndexedSearchRequest) (string, []any, error) {
	where, parameters, err := searchPredicates(criteria.Sessions, criteria.Events)
	if err != nil {
		return "", nil, err
	}
	parameters = append([]any{highlightStart, highlightEnd, quoteFTSLiteral(criteria.Text)}, parameters...)
	parameters = append(parameters, criteria.Limit, criteria.Offset)
	if len(parameters) > variableLimit {
		return "", nil, errors.New("session query sqlite: search exceeds SQLite variable limit")
	}
	statement := `
WITH matches AS (
  SELECT s.id, s.version, s.created_at, s.cwd, s.parent_session, s.seed_length,
         s.origin, s.delegation_depth, s.agent_preset, s.live, s.persisted,
         CAST(d.seq AS INTEGER) AS event_seq, d.type AS event_type,
         CAST(d.time AS INTEGER) AS event_time, d.surface AS event_surface,
         highlight(indexed_documents, 0, ?, ?) AS marked_text,
         bm25(indexed_documents) AS score
  FROM indexed_documents AS d
  JOIN indexed_sessions AS s ON s.id = d.session_id
  WHERE indexed_documents MATCH ?` + where + `
), ranked AS (
  SELECT *, row_number() OVER (
    PARTITION BY id ORDER BY score, event_time DESC, event_seq, id
  ) AS event_rank
  FROM matches
)
SELECT id, version, created_at, cwd, parent_session, seed_length, origin,
       delegation_depth, agent_preset, live, persisted,
       event_seq, event_type, event_time, event_surface, marked_text
FROM ranked
WHERE event_rank = 1
ORDER BY score, event_time DESC, id, event_seq
LIMIT ? OFFSET ?`
	return statement, parameters, nil
}

func buildEventSearch(criteria sessionquery.IndexedEventSearchRequest) (string, []any, error) {
	where, parameters, err := searchPredicates(sessionquery.SessionConstraints{
		IDs: []session.SessionID{criteria.SessionID},
	}, criteria.Events)
	if err != nil {
		return "", nil, err
	}
	parameters = append([]any{highlightStart, highlightEnd, quoteFTSLiteral(criteria.Text)}, parameters...)
	parameters = append(parameters, criteria.Limit, criteria.Offset)
	if len(parameters) > variableLimit {
		return "", nil, errors.New("session query sqlite: search exceeds SQLite variable limit")
	}
	statement := `
SELECT s.id, s.version, s.created_at, s.cwd, s.parent_session, s.seed_length,
       s.origin, s.delegation_depth, s.agent_preset, s.live, s.persisted,
       CAST(d.seq AS INTEGER) AS event_seq, d.type AS event_type,
       CAST(d.time AS INTEGER) AS event_time, d.surface AS event_surface,
       highlight(indexed_documents, 0, ?, ?) AS marked_text
FROM indexed_documents AS d
JOIN indexed_sessions AS s ON s.id = d.session_id
WHERE indexed_documents MATCH ?` + where + `
ORDER BY bm25(indexed_documents), event_time DESC, s.id, event_seq
LIMIT ? OFFSET ?`
	return statement, parameters, nil
}

func searchPredicates(
	sessions sessionquery.SessionConstraints,
	events sessionquery.EventConstraints,
) (string, []any, error) {
	clauses := make([]string, 0)
	parameters := make([]any, 0)
	if len(sessions.IDs) != 0 {
		values := make([]any, len(sessions.IDs))
		for position, identifier := range sessions.IDs {
			values[position] = string(identifier)
		}
		addListPredicate(&clauses, &parameters, "s.id", values)
	}
	if len(sessions.CWDs) != 0 {
		addNullableTextPredicate(&clauses, &parameters, "s.cwd", sessions.CWDs)
	}
	if sessions.CreatedAt != nil {
		addRangePredicate(&clauses, &parameters, "s.created_at", *sessions.CreatedAt)
	}
	if len(sessions.Parents) != 0 {
		values := make([]sessionquery.NullableText, len(sessions.Parents))
		for position, parent := range sessions.Parents {
			if parent.Value != nil {
				textValue := string(*parent.Value)
				values[position].Value = &textValue
			}
		}
		addNullableTextPredicate(&clauses, &parameters, "s.parent_session", values)
	}
	if len(sessions.Availability) == 1 {
		switch sessions.Availability[0] {
		case sessionquery.AvailabilityLive:
			clauses = append(clauses, "s.live = 1")
		case sessionquery.AvailabilityPersisted:
			clauses = append(clauses, "s.persisted = 1")
		default:
			return "", nil, errors.New("session query sqlite: unknown availability filter")
		}
	}
	if events.Sequences != nil {
		addRangePredicate(&clauses, &parameters, "CAST(d.seq AS INTEGER)", *events.Sequences)
	}
	if events.Times != nil {
		addRangePredicate(&clauses, &parameters, "CAST(d.time AS INTEGER)", *events.Times)
	}
	if len(events.Types) != 0 {
		values := make([]any, len(events.Types))
		for position, eventType := range events.Types {
			values[position] = eventType
		}
		addListPredicate(&clauses, &parameters, "d.type", values)
	}
	if len(events.Surfaces) != 0 {
		values := make([]any, len(events.Surfaces))
		for position, surface := range events.Surfaces {
			values[position] = string(surface)
		}
		addListPredicate(&clauses, &parameters, "d.surface", values)
	}
	if len(clauses) == 0 {
		return "", parameters, nil
	}
	return " AND " + strings.Join(clauses, " AND "), parameters, nil
}

func addListPredicate(clauses *[]string, parameters *[]any, column string, values []any) {
	placeholders := make([]string, len(values))
	for position, value := range values {
		placeholders[position] = "?"
		*parameters = append(*parameters, value)
	}
	*clauses = append(*clauses, column+" IN ("+strings.Join(placeholders, ", ")+")")
}

func addNullableTextPredicate(
	clauses *[]string,
	parameters *[]any,
	column string,
	values []sessionquery.NullableText,
) {
	nullIncluded := false
	nonNull := make([]any, 0, len(values))
	for _, value := range values {
		if value.Value == nil {
			nullIncluded = true
		} else {
			nonNull = append(nonNull, *value.Value)
		}
	}
	parts := make([]string, 0, 2)
	if nullIncluded {
		parts = append(parts, column+" IS NULL")
	}
	if len(nonNull) != 0 {
		placeholders := make([]string, len(nonNull))
		for position, value := range nonNull {
			placeholders[position] = "?"
			*parameters = append(*parameters, value)
		}
		parts = append(parts, column+" IN ("+strings.Join(placeholders, ", ")+")")
	}
	*clauses = append(*clauses, "("+strings.Join(parts, " OR ")+")")
}

func addRangePredicate(
	clauses *[]string,
	parameters *[]any,
	column string,
	interval sessionquery.Range,
) {
	if interval.From != nil {
		*clauses = append(*clauses, column+" >= ?")
		*parameters = append(*parameters, *interval.From)
	}
	if interval.To != nil {
		*clauses = append(*clauses, column+" <= ?")
		*parameters = append(*parameters, *interval.To)
	}
}

func scanSearchRow(source rowScanner, snippetLength int) (searchRow, error) {
	var row struct {
		id              string
		version         int64
		createdAt       int64
		cwd             sql.NullString
		parent          sql.NullString
		seedLength      sql.NullInt64
		origin          string
		delegationDepth sql.NullInt64
		agentPreset     sql.NullString
		live            int64
		persisted       int64
		sequence        int64
		eventType       string
		eventTime       int64
		surface         string
		markedText      string
	}
	if err := source.Scan(
		&row.id, &row.version, &row.createdAt, &row.cwd, &row.parent,
		&row.seedLength, &row.origin, &row.delegationDepth, &row.agentPreset,
		&row.live, &row.persisted, &row.sequence, &row.eventType,
		&row.eventTime, &row.surface, &row.markedText,
	); err != nil {
		return searchRow{}, err
	}
	indexed, err := indexedSessionFromRow(dbsqlIndexedSession(row.id, row.version, row.createdAt, row.cwd,
		row.parent, row.seedLength, row.origin, row.delegationDepth, row.agentPreset,
		row.live, row.persisted))
	if err != nil {
		return searchRow{}, err
	}
	surface := sessionquery.Surface(row.surface)
	if surface != sessionquery.SurfaceCurrent && surface != sessionquery.SurfaceShadowed && surface != sessionquery.SurfaceLogOnly {
		return searchRow{}, fmt.Errorf("session query sqlite: invalid indexed surface %q", row.surface)
	}
	return searchRow{
		header: indexed.Header, live: indexed.Live, persisted: indexed.Persisted,
		hit: sessionquery.EventHit{
			SessionID: indexed.Header.ID, Seq: row.sequence, Type: row.eventType,
			Time: row.eventTime, Surface: surface,
			Snippet: makeSnippet(row.markedText, snippetLength),
		},
	}, nil
}

func dbsqlIndexedSession(
	id string,
	version int64,
	createdAt int64,
	cwd sql.NullString,
	parent sql.NullString,
	seedLength sql.NullInt64,
	origin string,
	delegationDepth sql.NullInt64,
	agentPreset sql.NullString,
	live int64,
	persisted int64,
) dbsql.IndexedSession {
	return dbsql.IndexedSession{
		ID: id, Version: version, CreatedAt: createdAt, Cwd: cwd,
		ParentSession: parent, SeedLength: seedLength, Origin: origin,
		DelegationDepth: delegationDepth, AgentPreset: agentPreset,
		Live: live, Persisted: persisted, SourceRevision: "search-row", Generation: 1,
	}
}

func quoteFTSLiteral(textValue string) string {
	sanitized := sanitizeFTSText(textValue)
	return `"` + strings.ReplaceAll(sanitized, `"`, `""`) + `"`
}

func sanitizeFTSText(textValue string) string {
	result := strings.ReplaceAll(textValue, "\x00", "\uFFFD")
	result = strings.ReplaceAll(result, highlightStart, "\uFFFD")
	return strings.ReplaceAll(result, highlightEnd, "\uFFFD")
}

func makeSnippet(markedText string, maximum int) string {
	clean, matchStart := normalizeMarkedText(markedText)
	characters := []rune(clean)
	if len(characters) <= maximum {
		return clean
	}
	if maximum == 1 {
		return "…"
	}
	if matchStart >= len(characters) {
		matchStart = len(characters) - 1
	}
	start := max(0, matchStart-maximum/3)
	prefix := ""
	if start > 0 {
		prefix = "…"
	}
	suffix := "…"
	contentLength := maximum - len([]rune(prefix)) - len([]rune(suffix))
	if contentLength < 1 {
		start = matchStart
		suffix = ""
		contentLength = maximum - len([]rune(prefix))
	} else if matchStart >= start+contentLength {
		start = matchStart - contentLength + 1
	}
	end := min(len(characters), start+contentLength)
	if end == len(characters) {
		suffix = ""
		contentLength = maximum - len([]rune(prefix))
		start = max(0, end-contentLength)
	}
	end = min(len(characters), start+contentLength)
	return prefix + string(characters[start:end]) + suffix
}

func normalizeMarkedText(markedText string) (string, int) {
	characters := make([]rune, 0, len(markedText))
	matchStart := -1
	for _, character := range markedText {
		switch string(character) {
		case highlightStart:
			if matchStart < 0 {
				matchStart = len(characters)
			}
			continue
		case highlightEnd:
			continue
		}
		if unicode.IsSpace(character) {
			if len(characters) != 0 && characters[len(characters)-1] != ' ' {
				characters = append(characters, ' ')
			}
		} else {
			characters = append(characters, character)
		}
	}
	if len(characters) != 0 && characters[len(characters)-1] == ' ' {
		characters = characters[:len(characters)-1]
	}
	if matchStart < 0 {
		matchStart = 0
	}
	return string(characters), matchStart
}
