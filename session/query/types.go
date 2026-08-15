// Package query owns live-preferred Session search and the consumer-owned
// contract implemented by disposable full-text indexes.
package query

import (
	"context"

	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	sessiontitle "github.com/gorenx/goren/session/title"
)

const (
	// ServiceName is the canonical plugin service identity.
	ServiceName = "sessionQuery"

	DefaultLimit       = 20
	DefaultMaximum     = 100
	DefaultSnippetSize = 240
	DefaultReadWindow  = 50
	DefaultInspectors  = 4
)

// Surface identifies whether an event remains in model context, was
// shadowed by a replacement, or exists only in the raw log.
type Surface string

const (
	SurfaceCurrent  Surface = "current"
	SurfaceShadowed Surface = "shadowed"
	SurfaceLogOnly  Surface = "log-only"
)

// Cursor is an opaque continuation token owned by one Service instance.
type Cursor string

// Range is one inclusive optional numeric interval.
type Range struct {
	From *int64
	To   *int64
}

// SessionConstraints are ANDed metadata predicates. Values inside one slice
// are ORed; an empty slice means that predicate is absent.
type SessionConstraints struct {
	IDs          []session.SessionID
	CWDs         []NullableText
	CreatedAt    *Range
	Parents      []NullableSessionID
	Availability []Availability
}

// NullableText retains a deliberate SQL/null predicate without using magic
// sentinel strings.
type NullableText struct {
	Value *string
}

// NullableSessionID is the Session identity counterpart of NullableText.
type NullableSessionID struct {
	Value *session.SessionID
}

// Availability identifies a source currently backing one logical Session.
type Availability string

const (
	AvailabilityLive      Availability = "live"
	AvailabilityPersisted Availability = "persisted"
)

// EventConstraints are ANDed event metadata predicates.
type EventConstraints struct {
	Sequences *Range
	Times     *Range
	Types     []string
	Surfaces  []Surface
}

// SearchSessionsRequest asks for one ranked page grouped by Session.
type SearchSessionsRequest struct {
	Text     string
	Sessions SessionConstraints
	Events   EventConstraints
	Limit    int
	Cursor   Cursor
}

// SearchEventsRequest asks for one ranked page inside a Session.
type SearchEventsRequest struct {
	SessionID session.SessionID
	Text      string
	Events    EventConstraints
	Limit     int
	Cursor    Cursor
}

// SessionRecord is a detached live-preferred source observation.
type SessionRecord struct {
	Header    session.Header
	Live      bool
	Persisted bool
}

// LogSnapshot is one detached, replay-valid logical Session log.
type LogSnapshot struct {
	Header session.Header
	Events []session.Event
}

// EventRecord is lightweight surface-aware metadata for one raw event.
type EventRecord struct {
	SessionID session.SessionID
	Seq       int64
	Type      string
	Time      int64
	Surface   Surface
}

// SurfaceSnapshot captures one logical Session's current model-visible event
// surface and the raw-log boundary used to derive it.
type SurfaceSnapshot struct {
	Header             session.Header
	CapturedThroughSeq *int64
	Events             []session.Event
}

// EventReadRequest asks for one event and a bounded raw-log window.
type EventReadRequest struct {
	SessionID session.SessionID
	Seq       int64
	Before    int
	After     int
}

// EventWindow is one target and its contiguous raw-log context.
type EventWindow struct {
	Header   session.Header
	Target   session.Event
	Events   []session.Event
	StartSeq int64
	EndSeq   int64
}

// TitleObservation binds a folded title to the same header/log observation.
type TitleObservation struct {
	Header session.Header
	Title  *sessiontitle.Snapshot
}

// TitleObservationResult isolates one batch title failure from other ids.
type TitleObservationResult struct {
	SessionID   session.SessionID
	Observation *TitleObservation
	Err         error
}

// LineageNode is one recursive child in a Session relationship trace.
type LineageNode struct {
	Session     SessionRecord
	Descendants []LineageNode
}

// LineageTrace reports known ancestry and descendants. Root is present only
// when Complete is true; UnresolvedParent is present only when false.
type LineageTrace struct {
	Target           SessionRecord
	Ancestors        []SessionRecord
	Descendants      []LineageNode
	Complete         bool
	Root             *SessionRecord
	UnresolvedParent *session.SessionID
}

// EventTraceRequest identifies one raw event relationship target.
type EventTraceRequest struct {
	SessionID session.SessionID
	Seq       int64
}

// EventTrace reports direct positional replacement and explicit provenance
// edges recorded by the Session log.
type EventTrace struct {
	Header            session.Header
	Target            EventRecord
	ReplacedBy        *int64
	ReplacementChain  []int64
	ReplacedEventSeqs []int64
	SourceEventSeqs   []int64
	DerivedEventSeqs  []int64
}

// EventHit is one ranked semantic event match.
type EventHit struct {
	SessionID session.SessionID
	Seq       int64
	Type      string
	Time      int64
	Surface   Surface
	Snippet   string
}

// SessionHit groups one Session by its strongest event match.
type SessionHit struct {
	SessionRecord
	BestMatch EventHit
}

// SessionSearchPage is one cursor-paginated cross-Session result.
type SessionSearchPage struct {
	Items      []SessionHit
	NextCursor Cursor
}

// EventSearchPage binds event matches to the indexed header observation.
type EventSearchPage struct {
	Session    session.Header
	Items      []EventHit
	NextCursor Cursor
}

// QueryService is the stable capability consumed by API, export, and future
// Session query consumers. Concrete SQLite mechanics remain behind Index.
type QueryService interface {
	ListSessions(context.Context) ([]SessionRecord, error)
	ReadSession(context.Context, session.SessionID) (LogSnapshot, error)
	FilterSessions(context.Context, SessionConstraints) ([]SessionRecord, error)
	ReadTitle(context.Context, session.SessionID) (*sessiontitle.Snapshot, error)
	ReadTitleSnapshot(context.Context, session.SessionID) (TitleObservation, error)
	ReadTitleSnapshots(context.Context, []session.SessionID) ([]TitleObservationResult, error)
	ListEvents(context.Context, session.SessionID) ([]EventRecord, error)
	FilterEvents(context.Context, session.SessionID, EventConstraints, string) ([]Document, error)
	ReadSurface(context.Context, session.SessionID) (SurfaceSnapshot, error)
	ReadEvent(context.Context, EventReadRequest) (EventWindow, error)
	TraceSession(context.Context, session.SessionID) (LineageTrace, error)
	TraceEvent(context.Context, EventTraceRequest) (EventTrace, error)
	SearchSessions(context.Context, SearchSessionsRequest) (SessionSearchPage, error)
	SearchEvents(context.Context, SearchEventsRequest) (EventSearchPage, error)
}

// ServiceKey is the canonical Session Query service definition.
var ServiceKey = plugin.DefineService[QueryService](ServiceName)

// Document is one semantic, surface-aware projection stored in the derived
// index. It contains no persistence row or wire representation.
type Document struct {
	SessionID session.SessionID
	Seq       int64
	Type      string
	Time      int64
	Surface   Surface
	Text      string
}

// IndexedSession is lightweight source state already materialized by Index.
type IndexedSession struct {
	Header         session.Header
	Live           bool
	Persisted      bool
	SourceRevision string
	Generation     int64
}

// IndexSnapshot is the generation and source state observed atomically before
// reconciliation.
type IndexSnapshot struct {
	Generation int64
	Sessions   map[session.SessionID]IndexedSession
}

// Replacement atomically replaces one Session's index metadata and, when
// ReplaceDocuments is true, its complete semantic document set.
type Replacement struct {
	Session          IndexedSession
	ReplaceDocuments bool
	Documents        []Document
}

// Reconciliation is one complete logical-corpus delta.
type Reconciliation struct {
	Replace []Replacement
	Remove  []session.SessionID
}

// IndexedSearchRequest is the normalized query executed against one stable
// index generation.
type IndexedSearchRequest struct {
	Text     string
	Sessions SessionConstraints
	Events   EventConstraints
	Offset   int
	Limit    int
}

// IndexedEventSearchRequest is the within-Session index counterpart.
type IndexedEventSearchRequest struct {
	SessionID session.SessionID
	Text      string
	Events    EventConstraints
	Offset    int
	Limit     int
}

// Index is the storage-only derived-index port owned by Session Query.
// Implementations may persist cache state, but Session facts remain the only
// source of truth.
type Index interface {
	Snapshot(context.Context) (IndexSnapshot, error)
	Reconcile(context.Context, Reconciliation) (IndexSnapshot, error)
	SearchSessions(context.Context, IndexedSearchRequest) ([]SessionHit, error)
	SearchEvents(context.Context, IndexedEventSearchRequest) (session.Header, []EventHit, error)
	Close(context.Context) error
}
