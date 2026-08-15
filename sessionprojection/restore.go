package sessionprojection

import (
	"encoding/json"
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
	var floor *int64
	for _, projectionKey := range owner.order {
		entry := owner.registrations[projectionKey]
		if entry == nil {
			continue
		}
		needed := int64(0)
		if row, found := rows[projectionKey]; found && row.Version == entry.projectionUnit.StateVersion() {
			if row.Seq < math.MaxInt64 {
				needed = max(row.Seq+1, 0)
			}
		}
		if floor == nil || needed < *floor {
			candidate := needed
			floor = &candidate
		}
	}
	if floor == nil {
		return nil
	}
	anchor := max(*floor-1, 0)
	return &anchor
}

// ViewCheckpoint serves only version-compatible cached rows without log I/O.
func (owner *DriveRegistry) ViewCheckpoint(rows Checkpoint) (Values, error) {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	projectionValues := make(Values)
	for _, projectionKey := range owner.order {
		entry := owner.registrations[projectionKey]
		if entry == nil {
			continue
		}
		row, found := rows[projectionKey]
		if !found || row.Version != entry.projectionUnit.StateVersion() {
			continue
		}
		state, err := validatedRaw(row.Value)
		if err != nil {
			return nil, fmt.Errorf("sessionprojection: checkpoint %q state: %w", projectionKey, err)
		}
		view, err := viewValue(entry.projectionUnit, state)
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
		if entry == nil {
			continue
		}
		row, found := rows[projectionKey]
		usable := found && row.Version == entry.projectionUnit.StateVersion() &&
			row.Seq >= baseSeq-1 && row.Seq <= endSeq
		if !usable && baseSeq > 0 {
			return RestoreResult{}, fmt.Errorf(
				"sessionprojection: projection %q cannot restore from seq %d; re-read from seq 0",
				projectionKey, baseSeq,
			)
		}
		var state json.RawMessage
		fromSeq := baseSeq - 1
		if usable {
			state, err = validatedRaw(row.Value)
			fromSeq = row.Seq
		} else {
			state, err = entry.projectionUnit.InitialState()
			if err == nil {
				state, err = validatedRaw(state)
			}
		}
		if err != nil {
			return RestoreResult{}, fmt.Errorf("sessionprojection: projection %q restore state: %w", projectionKey, err)
		}
		for _, committed := range events {
			if committed.Seq <= fromSeq {
				continue
			}
			stateChange, transitionErr := applyTransition(entry.projectionUnit, state, committed)
			if transitionErr != nil {
				return RestoreResult{}, fmt.Errorf(
					"sessionprojection: projection %q restore seq %d: %w",
					projectionKey, committed.Seq, transitionErr,
				)
			}
			state = stateChange.State
		}
		view, viewErr := viewValue(entry.projectionUnit, state)
		if viewErr != nil {
			return RestoreResult{}, fmt.Errorf("sessionprojection: projection %q restore view: %w", projectionKey, viewErr)
		}
		projectionValues[projectionKey] = view
		refreshed[projectionKey] = CheckpointRow{
			Version: entry.projectionUnit.StateVersion(), Seq: endSeq, Value: cloneRaw(state),
		}
	}
	return RestoreResult{
		Snapshot:   Snapshot{AsOfSeq: endSeq, Values: projectionValues},
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
