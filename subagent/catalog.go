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
	// DiagnosticUnsupported is reserved for a consumer-routable future format.
	DiagnosticUnsupported DiagnosticReason = "unsupported"
	// DiagnosticUnavailable means a persistence read failed transiently.
	DiagnosticUnavailable DiagnosticReason = "unavailable"
)

// ListEntry is one interpreted direct-child row or contained diagnostic.
type ListEntry interface {
	listEntryVariant()
	SessionID() session.SessionID
}

// OneShotChildEntry is one descriptor-backed terminal child. Listing exposes
// only durable presentation identity, never Provider-private creation data.
type OneShotChildEntry struct {
	ID          session.SessionID
	Label       *string
	Activity    Activity
	HasChildren bool
}

func (OneShotChildEntry) listEntryVariant() {}

// SessionID returns the durable child identity.
func (entry OneShotChildEntry) SessionID() session.SessionID {
	return entry.ID
}

// ContinuableChildEntry is one descriptor-backed resumable child.
type ContinuableChildEntry struct {
	ID          session.SessionID
	Label       string
	Activity    Activity
	HasChildren bool
}

func (ContinuableChildEntry) listEntryVariant() {}

// SessionID returns the durable child identity.
func (entry ContinuableChildEntry) SessionID() session.SessionID {
	return entry.ID
}

// DiagnosticEntry contains one per-candidate listing failure.
type DiagnosticEntry struct {
	ID     session.SessionID
	Reason DiagnosticReason
}

func (DiagnosticEntry) listEntryVariant() {}

// SessionID returns the candidate identity.
func (entry DiagnosticEntry) SessionID() session.SessionID {
	return entry.ID
}

// DescendantListEntry positions one listing row in a complete descendant tree.
type DescendantListEntry struct {
	Entry    ListEntry
	ParentID session.SessionID
	Depth    int64
}

// Catalog enumerates durable Subagent identities without starting or resuming
// child Agents. It reports both one-shot and continuable records.
type Catalog interface {
	ListChildren(context.Context, session.SessionID) ([]ListEntry, error)
	ListDescendants(
		context.Context,
		session.SessionID,
	) ([]DescendantListEntry, error)
}
