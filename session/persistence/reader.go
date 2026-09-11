package persistence

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"

	"github.com/gorenx/goren/session"
)

// Reader validates durable read results without owning live Session state or storage lifetime.
type Reader struct {
	storage ReadStorage
}

// NewReader installs the storage-only read port.
func NewReader(backend ReadStorage) (*Reader, error) {
	if backend == nil {
		return nil, errors.New("session persistence: read storage is required")
	}
	return &Reader{
		storage: backend,
	}, nil
}

// Inspect returns validated durable facts without repairing or writing the log.
func (owner *Reader) Inspect(ctx context.Context, identifier session.SessionID) (Inspection, error) {
	if ctx == nil {
		return Inspection{}, errors.New("session persistence: Inspect Context is nil")
	}
	stored, err := owner.storage.Load(ctx, identifier)
	if err != nil {
		return Inspection{}, err
	}
	if stored == nil {
		return Inspection{}, &NotFoundError{
			ID: identifier,
		}
	}
	return owner.logicalInspection(identifier, *stored)
}

// Locate returns a backend-owned artifact location when available.
func (owner *Reader) Locate(metadata session.Header) (Location, bool) {
	return owner.storage.Locate(cloneHeader(metadata))
}

// SupportsRawArtifacts reports whether exact source text can be exported.
func (owner *Reader) SupportsRawArtifacts() bool {
	return owner.storage.SupportsRawArtifacts()
}

// ReadRaw returns detached metadata and exact stored text without interpreting it.
func (owner *Reader) ReadRaw(
	requestContext context.Context,
	identifier session.SessionID,
) (RawArtifact, error) {
	if requestContext == nil {
		return RawArtifact{}, errors.New("session persistence: ReadRaw Context is nil")
	}
	if !owner.storage.SupportsRawArtifacts() {
		return RawArtifact{}, errRawArtifactsUnavailable
	}
	artifact, err := owner.storage.ReadRaw(requestContext, identifier)
	if err != nil {
		return RawArtifact{}, err
	}
	if artifact == nil {
		return RawArtifact{}, &NotFoundError{ID: identifier}
	}
	detached := *artifact
	detached.Header = cloneHeader(artifact.Header)
	return detached, nil
}

// ReadEventsFrom validates an oldest-first page at the requested inclusive cursor.
func (owner *Reader) ReadEventsFrom(
	requestContext context.Context,
	identifier session.SessionID,
	continuation EventContinuation,
) (EventSegment, error) {
	if requestContext == nil {
		return EventSegment{}, errors.New("session persistence: ReadEventsFrom Context is nil")
	}
	if continuation.FromSeq < 0 {
		return EventSegment{}, errors.New("session persistence: event cursor must be non-negative")
	}
	if err := validatePageLimit(continuation.Limit); err != nil {
		return EventSegment{}, err
	}
	stored, err := owner.storage.ReadEventsFrom(requestContext, identifier, continuation)
	if err != nil {
		return EventSegment{}, err
	}
	if stored == nil {
		return EventSegment{}, &NotFoundError{ID: identifier}
	}
	if stored.Header.ID != identifier {
		return EventSegment{}, owner.corruption(identifier, fmt.Errorf(
			"stored identity mismatch: requested %q, header contains %q", identifier, stored.Header.ID,
		))
	}
	if stored.Header.Version != session.FormatVersion {
		return EventSegment{}, owner.normalizeLoadError(identifier, stored.Header, fmt.Errorf(
			"unsupported Session format v%d", stored.Header.Version,
		))
	}
	if int64(len(stored.Events)) > continuation.Limit || stored.HasMore && len(stored.Events) == 0 {
		return EventSegment{}, owner.corruption(identifier, errors.New(
			"stored event segment violates its page boundary",
		))
	}
	for position, entry := range stored.Events {
		expected := continuation.FromSeq + int64(position)
		if entry.Seq != expected {
			return EventSegment{}, owner.corruption(identifier, fmt.Errorf(
				"stored suffix seq mismatch: expected %d, got %d", expected, entry.Seq,
			))
		}
		if !session.IsKnownEventType(entry.Type) && !entry.Ignorable {
			return EventSegment{}, owner.normalizeLoadError(identifier, stored.Header, &UnsupportedFormatError{
				ID: identifier,
				Reason: fmt.Sprintf(
					"session %q contains event type %q at seq %d unknown to this harness and not marked ignorable",
					identifier, entry.Type, entry.Seq,
				),
			})
		}
	}
	return EventSegment{
		Header:   cloneHeader(stored.Header),
		Revision: stored.Revision,
		Events:   snapshotEvents(stored.Events),
		HasMore:  stored.HasMore,
	}, nil
}

// ReadEventsBefore returns at most maxEvents events immediately preceding the
// exclusive beforeSeq cursor. Nil beforeSeq reads from the durable log tail.
func (owner *Reader) ReadEventsBefore(
	requestContext context.Context,
	identifier session.SessionID,
	page EventPage,
) (EventWindow, error) {
	if requestContext == nil {
		return EventWindow{}, errors.New("session persistence: readEventsBefore Context is nil")
	}
	if page.BeforeSeq != nil && *page.BeforeSeq < 0 {
		return EventWindow{}, errors.New("session persistence: beforeSeq must be non-negative")
	}
	if err := validatePageLimit(page.Limit); err != nil {
		return EventWindow{}, err
	}
	stored, err := owner.storage.ReadEventsBefore(
		requestContext,
		identifier,
		page,
	)
	if err != nil {
		return EventWindow{}, err
	}
	if stored == nil {
		return EventWindow{}, &NotFoundError{ID: identifier}
	}
	if stored.Header.ID != identifier {
		return EventWindow{}, owner.corruption(identifier, fmt.Errorf(
			"stored identity mismatch: requested %q, header contains %q",
			identifier,
			stored.Header.ID,
		))
	}
	if stored.Header.Version != session.FormatVersion {
		return EventWindow{}, owner.normalizeLoadError(identifier, stored.Header, fmt.Errorf(
			"unsupported Session format v%d",
			stored.Header.Version,
		))
	}
	if int64(len(stored.Events)) > page.Limit || stored.HasEarlier && len(stored.Events) == 0 {
		return EventWindow{}, owner.corruption(identifier, errors.New(
			"stored event window violates its page boundary",
		))
	}
	if err := validateEventWindow(identifier, stored.Events, page.BeforeSeq); err != nil {
		return EventWindow{}, owner.corruption(identifier, err)
	}
	for _, entry := range stored.Events {
		if !session.IsKnownEventType(entry.Type) && !entry.Ignorable {
			return EventWindow{}, owner.normalizeLoadError(identifier, stored.Header, &UnsupportedFormatError{
				ID: identifier,
				Reason: fmt.Sprintf(
					"session %q contains event type %q at seq %d unknown to this harness and not marked ignorable",
					identifier,
					entry.Type,
					entry.Seq,
				),
			})
		}
	}
	return EventWindow{
		Header:     cloneHeader(stored.Header),
		Events:     snapshotEvents(stored.Events),
		HasEarlier: stored.HasEarlier,
	}, nil
}

func validateEventWindow(
	identifier session.SessionID,
	entries []session.Event,
	beforeSeq *int64,
) error {
	for index, entry := range entries {
		if entry.Seq < 0 {
			return errors.New("stored event window contains a negative sequence")
		}
		if index > 0 && entry.Seq != entries[index-1].Seq-1 {
			return fmt.Errorf(
				"stored event window for %q is not contiguous at seq %d",
				identifier,
				entry.Seq,
			)
		}
		if beforeSeq != nil && entry.Seq >= *beforeSeq {
			return fmt.Errorf(
				"stored event window for %q crossed exclusive beforeSeq %d",
				identifier,
				*beforeSeq,
			)
		}
	}
	return nil
}

func validatePageLimit(limit int64) error {
	if limit < 1 {
		return errors.New("session persistence: page limit must be positive")
	}
	if limit == math.MaxInt64 {
		return errors.New("session persistence: page limit is too large")
	}
	return nil
}

// List returns validated headers in stable newest-first order.
func (owner *Reader) List(
	requestContext context.Context,
	page SessionPage,
) (HeaderPage, error) {
	snapshots, err := owner.ListSnapshots(requestContext, page)
	if err != nil {
		return HeaderPage{}, err
	}
	result := HeaderPage{
		Headers:    make([]session.Header, len(snapshots.Snapshots)),
		NextCursor: cloneSessionCursor(snapshots.NextCursor),
	}
	for index, storedSnapshot := range snapshots.Snapshots {
		result.Headers[index] = cloneHeader(storedSnapshot.Header)
	}
	return result, nil
}

// ListSnapshots returns detached headers and revisions with a validated cursor.
func (owner *Reader) ListSnapshots(
	requestContext context.Context,
	page SessionPage,
) (SnapshotPage, error) {
	if requestContext == nil {
		return SnapshotPage{}, errors.New("session persistence: ListSnapshots Context is nil")
	}
	if err := validatePageLimit(page.Limit); err != nil {
		return SnapshotPage{}, err
	}
	if page.Cursor != nil && (page.Cursor.CreatedAt < 0 || page.Cursor.ID == "") {
		return SnapshotPage{}, errors.New("session persistence: Session cursor is invalid")
	}
	stored, err := owner.storage.List(requestContext, page)
	if err != nil {
		return SnapshotPage{}, err
	}
	if err := validateSnapshotPage(page, stored); err != nil {
		return SnapshotPage{}, err
	}
	result := SnapshotPage{
		Snapshots:  make([]Snapshot, len(stored.Snapshots)),
		NextCursor: cloneSessionCursor(stored.NextCursor),
	}
	for index, storedSnapshot := range stored.Snapshots {
		result.Snapshots[index] = Snapshot{
			Header:   cloneHeader(storedSnapshot.Header),
			Revision: storedSnapshot.Revision,
		}
	}
	return result, nil
}

func validateSnapshotPage(request SessionPage, result SnapshotPage) error {
	if int64(len(result.Snapshots)) > request.Limit {
		return errors.New("session persistence: Backend returned more Sessions than requested")
	}
	previous := request.Cursor
	for _, storedSnapshot := range result.Snapshots {
		if err := validateDetachedHeader(storedSnapshot.Header.ID, storedSnapshot.Header); err != nil {
			return err
		}
		if previous != nil && !sessionFollows(*previous, storedSnapshot.Header) {
			return errors.New("session persistence: Backend returned an unordered Session page")
		}
		previous = &SessionCursor{
			CreatedAt: storedSnapshot.Header.CreatedAt,
			ID:        storedSnapshot.Header.ID,
		}
	}
	if result.NextCursor == nil {
		return nil
	}
	if len(result.Snapshots) == 0 {
		return errors.New("session persistence: Backend returned a continuation cursor for an empty page")
	}
	last := result.Snapshots[len(result.Snapshots)-1].Header
	if result.NextCursor.CreatedAt != last.CreatedAt || result.NextCursor.ID != last.ID {
		return errors.New("session persistence: Backend continuation cursor does not match the page tail")
	}
	return nil
}

func sessionFollows(cursor SessionCursor, candidate session.Header) bool {
	return candidate.CreatedAt < cursor.CreatedAt ||
		candidate.CreatedAt == cursor.CreatedAt && candidate.ID > cursor.ID
}

func cloneSessionCursor(cursor *SessionCursor) *SessionCursor {
	if cursor == nil {
		return nil
	}
	copyValue := *cursor
	return &copyValue
}

func (owner *Reader) logicalInspection(identifier session.SessionID, stored Log) (Inspection, error) {
	if stored.Header.ID != identifier {
		return Inspection{}, owner.corruption(identifier, fmt.Errorf(
			"stored identity mismatch: requested %q, header contains %q", identifier, stored.Header.ID,
		))
	}
	loaded, err := inspectStored(stored.Header, stored.Events)
	if err != nil {
		return Inspection{}, owner.normalizeLoadError(identifier, stored.Header, err)
	}
	return loaded, nil
}

func (owner *Reader) normalizeLoadError(identifier session.SessionID, metadata session.Header, problem error) error {
	var unsupported *UnsupportedFormatError
	if errors.As(problem, &unsupported) {
		if unsupported.Location == nil {
			if artifactLocation, found := owner.storage.Locate(metadata); found {
				unsupported.Location = &artifactLocation
			}
		}
		return unsupported
	}
	if metadata.Version != session.FormatVersion {
		reason := formatVersionRefusal(identifier, metadata.Version)
		refusal := &UnsupportedFormatError{
			ID:     identifier,
			Reason: reason,
		}
		if artifactLocation, found := owner.storage.Locate(metadata); found {
			refusal.Location = &artifactLocation
		}
		return refusal
	}
	return owner.corruption(identifier, problem)
}

func (owner *Reader) corruption(identifier session.SessionID, problem error) error {
	var storedFailure *CorruptionError
	if errors.As(problem, &storedFailure) {
		return problem
	}
	return &CorruptionError{
		ID:    identifier,
		Cause: problem,
	}
}

func validateDetachedHeader(identifier session.SessionID, metadata session.Header) error {
	conversation, err := session.NewLog(identifier, session.CreateOptions{
		Metadata: metadataFromHeader(metadata),
	})
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(conversation.Header(), metadata) {
		return errors.New("session persistence: header is not canonical")
	}
	return nil
}

func metadataFromHeader(metadata session.Header) session.Metadata {
	return session.Metadata{
		CreatedAt:       int64Pointer(metadata.CreatedAt),
		CWD:             cloneTextPointer(metadata.CWD),
		ParentSession:   cloneSessionPointer(metadata.ParentSession),
		SeedLength:      cloneInt64Pointer(metadata.SeedLength),
		Origin:          metadata.Origin,
		DelegationDepth: cloneInt64Pointer(metadata.DelegationDepth),
		AgentPreset:     cloneTextPointer(metadata.AgentPreset),
	}
}

func formatVersionRefusal(identifier session.SessionID, version int) string {
	if version > session.FormatVersion {
		return fmt.Sprintf(
			"session %q uses log format v%d, but this harness reads only v%d; upgrade the harness to open it",
			identifier, version, session.FormatVersion,
		)
	}
	return fmt.Sprintf(
		"session %q uses log format v%d, older than supported v%d, and this build ships no upgrade path",
		identifier, version, session.FormatVersion,
	)
}
