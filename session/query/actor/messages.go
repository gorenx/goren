// Package queryactor exposes Session Query through named actor commands.
package queryactor

import (
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/session/query"
	"github.com/gorenx/goren/session/title"
)

// Observe replaces the complete successfully acquired source observation.
type Observe struct {
	Sources []query.Source
}

// Completion reports whether the source observation was accepted.
type Completion struct {
	Err error
}

// ListSessions requests all observed sessions.
type ListSessions struct{}

// FilterSessions selects observed sessions by metadata.
type FilterSessions struct {
	Constraints query.SessionConstraints
}

// Sessions returns detached Session metadata and availability.
type Sessions struct {
	Items []query.SessionRecord
	Err   error
}

// ReadSession requests the selected live-preferred log.
type ReadSession struct {
	SessionID session.SessionID
}

// Log returns a detached Session observation.
type Log struct {
	Value query.LogSnapshot
	Err   error
}

// ReadTitle requests the latest committed title from the observation.
type ReadTitle struct {
	SessionID session.SessionID
}

// Title returns a title, or nil when no title exists.
type Title struct {
	Value *title.Snapshot
	Err   error
}

// ReadTitles reads one title observation for each unique Session identity.
type ReadTitles struct {
	SessionIDs []session.SessionID
}

// Titles preserves per-Session errors separately from whole-request cancellation.
type Titles struct {
	Items []query.TitleObservationResult
	Err   error
}

// ListEvents requests raw-event metadata and surface classification.
type ListEvents struct {
	SessionID session.SessionID
}

// Events returns detached raw-event metadata.
type Events struct {
	Items []query.EventRecord
	Err   error
}

// FilterEvents filters an observed log by event metadata and literal text.
type FilterEvents struct {
	SessionID   session.SessionID
	Constraints query.EventConstraints
	Text        string
}

// Documents returns semantic event documents matching an exact filter.
type Documents struct {
	Items []query.Document
	Err   error
}

// ReadSurface requests the current model-visible event surface.
type ReadSurface struct {
	SessionID session.SessionID
}

// Surface returns the observed model-visible prefix.
type Surface struct {
	Value query.SurfaceSnapshot
	Err   error
}

// ReadEvent requests an event and its bounded raw-log context.
type ReadEvent struct {
	Request query.EventReadRequest
}

// Event returns an event window.
type Event struct {
	Value query.EventWindow
	Err   error
}

// TraceSession requests ancestry and descendants in the observed corpus.
type TraceSession struct {
	SessionID session.SessionID
}

// Lineage returns Session relationships.
type Lineage struct {
	Value query.LineageTrace
	Err   error
}

// TraceEvent requests replacement and provenance relationships.
type TraceEvent struct {
	Request query.EventTraceRequest
}

// Trace returns the event's direct and transitive relationships.
type Trace struct {
	Value query.EventTrace
	Err   error
}

// SearchSessions searches the observed corpus through the derived index.
type SearchSessions struct {
	Request query.SearchSessionsRequest
}

// SessionMatches returns one ranked page and its continuation cursor.
type SessionMatches struct {
	Page query.SessionSearchPage
	Err  error
}

// SearchEvents searches within one observed Session.
type SearchEvents struct {
	Request query.SearchEventsRequest
}

// EventMatches returns a ranked page bound to its indexed Session header.
type EventMatches struct {
	Page query.EventSearchPage
	Err  error
}
