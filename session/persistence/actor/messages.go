package persistenceactor

import (
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/session/persistence"
)

// Inspect requests the validated logical log without repair or activation.
type Inspect struct {
	SessionID session.SessionID
}

// Inspection returns the detached logical log or its typed failure.
type Inspection struct {
	Value persistence.Inspection
	Err   error
}

// ListSnapshots requests one durable metadata page.
type ListSnapshots struct {
	Page persistence.SessionPage
}

// Snapshots returns a bounded metadata page and its continuation.
type Snapshots struct {
	Value persistence.SnapshotPage
	Err   error
}

// ReadEventsFrom requests an inclusive oldest-first event page.
type ReadEventsFrom struct {
	SessionID session.SessionID
	Page      persistence.EventContinuation
}

// Segment returns events and the source revision observed by the read.
type Segment struct {
	Value persistence.EventSegment
	Err   error
}

// ReadEventsBefore requests events preceding an exclusive cursor.
type ReadEventsBefore struct {
	SessionID session.SessionID
	Page      persistence.EventPage
}

// Window returns a newest-first event page.
type Window struct {
	Value persistence.EventWindow
	Err   error
}
