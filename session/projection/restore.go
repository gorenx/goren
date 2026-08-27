package projection

import (
	"errors"
	"fmt"
	"math"

	"github.com/gorenx/goren/session"
)

// RestoreFloor returns the one-below tail-read anchor required by the lowest
// usable checkpoint row. Nil means no projection unit is registered.
func (owner *DriveRegistry) RestoreFloor(rows Checkpoint) *int64 {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if len(owner.order) == 0 {
		return nil
	}
	floor := int64(math.MaxInt64)
	for _, projectionKey := range owner.order {
		entry := owner.registrations[projectionKey]
		needed := int64(0)
		if row, found := rows[projectionKey]; found && row.Version == entry.projectionUnit.StateVersion() {
			if row.Seq < math.MaxInt64 {
				needed = max(row.Seq+1, 0)
			}
		}
		floor = min(floor, needed)
	}
	anchor := max(floor-1, 0)
	return &anchor
}

// ViewCheckpoint serves only version-compatible cached rows without log I/O.
func (owner *DriveRegistry) ViewCheckpoint(rows Checkpoint) (Values, error) {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	projectionValues := make(Values)
	for _, projectionKey := range owner.order {
		entry := owner.registrations[projectionKey]
		row, found := rows[projectionKey]
		if !found || row.Version != entry.projectionUnit.StateVersion() {
			continue
		}
		if err := validateRaw(row.Value); err != nil {
			return nil, fmt.Errorf("sessionprojection: checkpoint %q state: %w", projectionKey, err)
		}
		view, err := viewValue(entry.projectionUnit, row.Value)
		if err != nil {
			return nil, fmt.Errorf("sessionprojection: checkpoint %q view: %w", projectionKey, err)
		}
		projectionValues[projectionKey] = view
	}
	return projectionValues, nil
}

// Restore folds registered units over a stored suffix and refreshes their
// non-authoritative checkpoint rows.
func (owner *DriveRegistry) Restore(rows Checkpoint, events []session.Event, baseSeq int64) (RestoreResult, error) {
	if baseSeq < 0 {
		return RestoreResult{}, errors.New("sessionprojection: restore baseSeq must be non-negative")
	}
	endSeq, err := validateRestoreEvents(events, baseSeq)
	if err != nil {
		return RestoreResult{}, err
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	projectionValues := make(Values, len(owner.registrations))
	refreshed := make(Checkpoint, len(owner.registrations))
	for _, projectionKey := range owner.order {
		entry := owner.registrations[projectionKey]
		row, found := rows[projectionKey]
		usable := found && row.Version == entry.projectionUnit.StateVersion() &&
			row.Seq >= baseSeq-1 && row.Seq <= endSeq
		if !usable && baseSeq > 0 {
			return RestoreResult{}, fmt.Errorf(
				"sessionprojection: projection %q cannot restore from seq %d; re-read from seq 0",
				projectionKey, baseSeq,
			)
		}
		var cell unitCell
		if usable {
			cell.state, err = validatedRaw(row.Value)
			cell.observedSeq = row.Seq
		} else {
			cell, err = buildCell(entry.projectionUnit, nil)
		}
		if err != nil {
			return RestoreResult{}, fmt.Errorf("sessionprojection: projection %q restore state: %w", projectionKey, err)
		}
		advanced, err := advanceCell(entry.projectionUnit, cell, events)
		if err != nil {
			return RestoreResult{}, fmt.Errorf(
				"sessionprojection: projection %q restore: %w",
				projectionKey,
				err,
			)
		}
		cell = advanced.cell
		view, viewErr := viewValue(entry.projectionUnit, cell.state)
		if viewErr != nil {
			return RestoreResult{}, fmt.Errorf("sessionprojection: projection %q restore view: %w", projectionKey, viewErr)
		}
		projectionValues[projectionKey] = view
		refreshed[projectionKey] = CheckpointRow{
			Version: entry.projectionUnit.StateVersion(),
			Seq:     endSeq,
			Value:   cloneRaw(cell.state),
		}
	}
	return RestoreResult{
		Snapshot: Snapshot{
			AsOfSeq: endSeq,
			Values:  projectionValues,
		},
		Checkpoint: refreshed,
	}, nil
}

func validateRestoreEvents(events []session.Event, baseSeq int64) (int64, error) {
	if len(events) == 0 {
		return baseSeq - 1, nil
	}
	expected := baseSeq
	for _, committed := range events {
		if committed.Seq != expected {
			return 0, fmt.Errorf(
				"sessionprojection: restore events are not contiguous at seq %d; expected %d",
				committed.Seq, expected,
			)
		}
		expected++
	}
	return events[len(events)-1].Seq, nil
}
