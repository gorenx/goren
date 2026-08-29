package subagent

import (
	"context"

	"github.com/gorenx/goren/session"
)

// Activity is a listing-time snapshot, not a durable outcome.
type Activity string

const (
	// ActivityRunning means the child Session is in the live store.
	ActivityRunning Activity = "running"
	// ActivityInactive means the child exists only in persistence.
	ActivityInactive Activity = "inactive"
)

// DiagnosticReason explains why a candidate cannot be presented as a child.
type DiagnosticReason string

const (
	// DiagnosticCorrupt means the durable identity could not be trusted.
	DiagnosticCorrupt DiagnosticReason = "corrupt"
	// DiagnosticUnavailable means a persistence read failed transiently.
	DiagnosticUnavailable DiagnosticReason = "unavailable"
)

// ChildEntry is one interpreted direct-child row or contained diagnostic.
type ChildEntry interface {
	listEntryVariant()
}

// OneShotChildEntry is one descriptor-backed terminal child. Listing exposes
// only durable presentation identity, never SeedBuilder-specific creation data.
type OneShotChildEntry struct {
	ID          session.SessionID
	Label       *string
	Activity    Activity
	HasChildren bool
}

func (OneShotChildEntry) listEntryVariant() {}

// ContinuableChildEntry is one descriptor-backed resumable child.
type ContinuableChildEntry struct {
	ID          session.SessionID
	Label       string
	Activity    Activity
	HasChildren bool
}

func (ContinuableChildEntry) listEntryVariant() {}

// BoundChildEntry is one descriptor-backed child initialized from a durable
// parent binding.
type BoundChildEntry struct {
	ID          session.SessionID
	Label       string
	Activity    Activity
	HasChildren bool
}

func (BoundChildEntry) listEntryVariant() {}

// DiagnosticEntry contains one per-candidate listing failure.
type DiagnosticEntry struct {
	ID     session.SessionID
	Reason DiagnosticReason
}

func (DiagnosticEntry) listEntryVariant() {}

// DescendantEntry positions one child entry in a complete descendant tree.
type DescendantEntry struct {
	Entry    ChildEntry
	ParentID session.SessionID
	Depth    int64
}

// ChildDirectory enumerates durable Subagent identities without starting or resuming
// child Agents. It reports OneShot, Continuable, and Bound records.
type ChildDirectory interface {
	ListChildren(context.Context, session.SessionID) ([]ChildEntry, error)
	ListDescendants(
		context.Context,
		session.SessionID,
	) ([]DescendantEntry, error)
}
