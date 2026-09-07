package projectioncache

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/gorenx/goren/internal/jsonvalue"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/session/persistence"
	"github.com/gorenx/goren/session/projection"
)

// PageSize bounds durable event pages during projection reconstruction.
const PageSize int64 = 512

// ErrCheckpointIdentity means the checkpoint belongs to another persistent log.
var ErrCheckpointIdentity = errors.New("session projection cache: checkpoint belongs to another log")

// View contains a projection proven against one durable Session revision.
type View struct {
	Header     session.Header
	Snapshot   projection.Snapshot
	Checkpoint projection.Checkpoint
}

// Replay accumulates a paged projection reconstruction without performing I/O.
type Replay struct {
	identifier session.SessionID
	cursor     int64
	checkpoint *CheckpointRecord
	metadata   *session.Header
	revision   persistence.Revision
	snapshot   projection.Snapshot
	complete   bool
}

// NewReplay fixes the Session and starting cut selected by the projection definitions.
func NewReplay(identifier session.SessionID, initial *CheckpointRecord, fromSeq int64) (*Replay, error) {
	if identifier == "" || fromSeq < 0 {
		return nil, errors.New("session projection cache: invalid replay origin")
	}
	if initial == nil && fromSeq != 0 {
		return nil, errors.New("session projection cache: partial replay requires a checkpoint")
	}
	var detached *CheckpointRecord
	if initial != nil {
		validated, err := ValidateCheckpointRecord(identifier, *initial)
		if err != nil {
			return nil, err
		}
		detached = &validated
	}
	return &Replay{
		identifier: identifier,
		cursor:     fromSeq,
		checkpoint: detached,
	}, nil
}

// Next returns the next storage page request; false means reconstruction is complete.
func (owner *Replay) Next() (persistence.EventContinuation, bool) {
	return persistence.EventContinuation{
		FromSeq: owner.cursor,
		Limit:   PageSize,
	}, !owner.complete
}

// Checkpoint returns detached accumulated rows for the next projection computation.
func (owner *Replay) Checkpoint() projection.Checkpoint {
	if owner.checkpoint == nil {
		return nil
	}
	return cloneRecord(*owner.checkpoint).Rows
}

// ValidatePage checks log identity, revision, and continuity before projection work.
func (owner *Replay) ValidatePage(page persistence.EventSegment) error {
	if owner.complete {
		return errors.New("session projection cache: replay is complete")
	}
	if page.Header.ID != owner.identifier {
		return errors.New("session projection cache: page belongs to another Session")
	}
	if owner.metadata != nil {
		if !reflect.DeepEqual(*owner.metadata, page.Header) || owner.revision != page.Revision {
			return errors.New("session projection cache: durable log changed during replay")
		}
	} else if owner.checkpoint != nil && !identityMatchesHeader(owner.checkpoint.Identity, page.Header) {
		return ErrCheckpointIdentity
	}
	if int64(len(page.Events)) > PageSize {
		return errors.New("session projection cache: replay page exceeds requested limit")
	}
	if page.HasMore && len(page.Events) == 0 {
		return errors.New("session projection cache: continued replay page is empty")
	}
	for index, entry := range page.Events {
		if entry.Seq != owner.cursor+int64(index) {
			return fmt.Errorf("session projection cache: replay expected sequence %d, got %d", owner.cursor+int64(index), entry.Seq)
		}
	}
	return nil
}

// Accept installs one validated page and its complete projection result atomically.
func (owner *Replay) Accept(page persistence.EventSegment, result projection.RestoreResult) error {
	if err := owner.ValidatePage(page); err != nil {
		return err
	}
	end := owner.cursor + int64(len(page.Events)) - 1
	if result.Snapshot.AsOfSeq != end {
		return errors.New("session projection cache: restored snapshot has a different cut")
	}
	for _, row := range result.Checkpoint {
		if row.Seq != end {
			return errors.New("session projection cache: restored checkpoint has a different cut")
		}
	}
	record, err := ValidateCheckpointRecord(owner.identifier, CheckpointRecord{
		Identity: identityOf(page.Header),
		Rows:     result.Checkpoint,
	})
	if err != nil {
		return err
	}
	if len(result.Snapshot.Values) != len(record.Rows) {
		return errors.New("session projection cache: restored views do not cover all checkpoint rows")
	}
	values := make(projection.Values, len(result.Snapshot.Values))
	for key, value := range result.Snapshot.Values {
		if _, found := record.Rows[key]; !found {
			return errors.New("session projection cache: restored view has no checkpoint row")
		}
		if jsonvalue.Validate(value) != nil {
			return errors.New("session projection cache: restored view is invalid JSON")
		}
		values[key] = append(json.RawMessage(nil), value...)
	}
	metadata := page.Header.Clone()
	owner.metadata = &metadata
	owner.revision = page.Revision
	owner.checkpoint = &record
	owner.snapshot = projection.Snapshot{
		AsOfSeq: end,
		Values:  values,
	}
	owner.cursor = end + 1
	owner.complete = !page.HasMore
	return nil
}

// Result returns detached output only after the final durable page was accepted.
func (owner *Replay) Result() (View, error) {
	if !owner.complete {
		return View{}, errors.New("session projection cache: replay has not reached the durable end")
	}
	values := make(projection.Values, len(owner.snapshot.Values))
	for key, value := range owner.snapshot.Values {
		values[key] = append(json.RawMessage(nil), value...)
	}
	return View{
		Header: owner.metadata.Clone(),
		Snapshot: projection.Snapshot{
			AsOfSeq: owner.snapshot.AsOfSeq,
			Values:  values,
		},
		Checkpoint: cloneRecord(*owner.checkpoint).Rows,
	}, nil
}
