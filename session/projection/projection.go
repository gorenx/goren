package projection

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gorenx/goren/internal/jsonvalue"
	"github.com/gorenx/goren/session"
)

// Projection owns the derived state of one log, independent of Actor lifetimes.
// It references its definitions; the Registry never refers back to projections.
type Projection struct {
	registry *Registry
	// cells maps a live Unit registration object to its JSON state and cursor.
	// Re-registering the same key produces a different object and cannot reuse state.
	cells map[*registration]unitCell
}

type unitCell struct {
	state       json.RawMessage
	observedSeq int64
}

type cellCandidate struct {
	key  string
	cell unitCell
}

type cellAdvance struct {
	cell            unitCell
	stateWasChanged bool
}

type eventCandidate struct {
	prepared        cellCandidate
	stateWasChanged bool
}

type eventPrefix struct {
	history  []session.Event
	boundary int64
	events   []session.Event
	loaded   bool
}

func (source *eventPrefix) load() []session.Event {
	if !source.loaded {
		source.events = eventsBefore(source.history, source.boundary)
		source.loaded = true
	}
	return source.events
}

// Snapshot folds missing cells lazily and serves one validated current state.
func (owner *Projection) Snapshot(events []session.Event) (Snapshot, error) {
	if err := validateHistory(events); err != nil {
		return Snapshot{}, err
	}
	candidates, err := owner.prepareCells(events)
	if err != nil {
		return Snapshot{}, err
	}
	projectionValues := make(Values, len(candidates))
	for _, prepared := range candidates {
		entry := owner.registry.registrations[prepared.key]
		view, err := viewValue(entry.projectionUnit, prepared.cell.state)
		if err != nil {
			return Snapshot{}, fmt.Errorf(
				"sessionprojection: projection %q view: %w",
				prepared.key,
				err,
			)
		}
		projectionValues[prepared.key] = view
	}
	owner.installCells(candidates)
	return Snapshot{
		AsOfSeq: lastSequence(events),
		Values:  projectionValues,
	}, nil
}

// Checkpoint returns detached rebuildable state for every registered unit.
func (owner *Projection) Checkpoint(events []session.Event) (Checkpoint, error) {
	if err := validateHistory(events); err != nil {
		return nil, err
	}
	candidates, err := owner.prepareCells(events)
	if err != nil {
		return nil, err
	}
	rows := make(Checkpoint, len(candidates))
	for _, prepared := range candidates {
		entry := owner.registry.registrations[prepared.key]
		rows[prepared.key] = CheckpointRow{
			Version: entry.projectionUnit.StateVersion(),
			Seq:     prepared.cell.observedSeq,
			Value:   cloneRaw(prepared.cell.state),
		}
	}
	owner.installCells(candidates)
	return rows, nil
}

// Observe advances all units atomically and returns detached changes.
func (owner *Projection) Observe(events []session.Event, seq int64) ([]Change, error) {
	if err := validateHistory(events); err != nil {
		return nil, err
	}
	if seq < 0 || seq >= int64(len(events)) {
		return nil, errors.New("sessionprojection: observed sequence is outside history")
	}
	committed := events[seq]
	return owner.observe(events, committed)
}

func (owner *Projection) observe(events []session.Event, committed session.Event) ([]Change, error) {
	owner.prune()
	candidates := make([]cellCandidate, 0, len(owner.registry.registrations))
	changes := make([]Change, 0)
	prefix := eventPrefix{
		history:  events,
		boundary: committed.Seq,
	}
	for _, projectionKey := range owner.registry.order {
		entry := owner.registry.registrations[projectionKey]
		staged, err := owner.prepareEventCandidate(
			projectionKey,
			committed,
			&prefix,
		)
		if err != nil {
			return nil, err
		}
		if staged == nil {
			continue
		}
		candidates = append(candidates, staged.prepared)
		if !staged.stateWasChanged {
			continue
		}
		view, err := viewValue(entry.projectionUnit, staged.prepared.cell.state)
		if err != nil {
			return nil, fmt.Errorf("sessionprojection: projection %q view seq %d: %w", projectionKey, committed.Seq, err)
		}
		changes = append(changes, Change{
			Key:   projectionKey,
			Value: view,
			Seq:   committed.Seq,
		})
	}
	owner.installCells(candidates)
	return changes, nil
}

func (owner *Projection) prepareEventCandidate(
	projectionKey string,
	committed session.Event,
	prefix *eventPrefix,
) (*eventCandidate, error) {
	entry := owner.registry.registrations[projectionKey]
	cell, found := owner.cells[entry]
	stateWasChanged := false
	switch {
	case !found:
		built, err := buildCell(entry.projectionUnit, prefix.load())
		if err != nil {
			return nil, fmt.Errorf(
				"sessionprojection: projection %q late fold: %w",
				projectionKey,
				err,
			)
		}
		cell = built
	case cell.observedSeq < committed.Seq-1:
		catchUp, err := advanceCell(entry.projectionUnit, cell, prefix.load())
		if err != nil {
			return nil, fmt.Errorf(
				"sessionprojection: projection %q catch up before seq %d: %w",
				projectionKey,
				committed.Seq,
				err,
			)
		}
		cell = catchUp.cell
		stateWasChanged = catchUp.stateWasChanged
	}
	if cell.observedSeq >= committed.Seq {
		return nil, nil
	}
	stateChange, err := applyTransition(entry.projectionUnit, cell.state, committed)
	if err != nil {
		return nil, fmt.Errorf(
			"sessionprojection: projection %q apply seq %d: %w",
			projectionKey,
			committed.Seq,
			err,
		)
	}
	cell.state = stateChange.State
	cell.observedSeq = committed.Seq
	return &eventCandidate{
		prepared: cellCandidate{
			key:  projectionKey,
			cell: cell,
		},
		stateWasChanged: stateWasChanged || stateChange.Changed,
	}, nil
}

func (owner *Projection) prepareCells(
	events []session.Event,
) ([]cellCandidate, error) {
	owner.prune()
	candidates := make([]cellCandidate, 0, len(owner.registry.registrations))
	for _, projectionKey := range owner.registry.order {
		entry := owner.registry.registrations[projectionKey]
		cell, found := owner.cells[entry]
		var prepared unitCell
		var err error
		if found && cell.observedSeq <= lastSequence(events) {
			var advanced cellAdvance
			advanced, err = advanceCell(entry.projectionUnit, cell, events)
			prepared = advanced.cell
		} else {
			prepared, err = buildCell(entry.projectionUnit, events)
		}
		if err != nil {
			return nil, fmt.Errorf(
				"sessionprojection: projection %q fold: %w",
				projectionKey,
				err,
			)
		}
		candidates = append(candidates, cellCandidate{
			key:  projectionKey,
			cell: prepared,
		})
	}
	return candidates, nil
}

func (owner *Projection) installCells(
	candidates []cellCandidate,
) {
	for _, prepared := range candidates {
		cells := owner.cells
		key := owner.registry.registrations[prepared.key]
		if previous, found := cells[key]; !found || previous.observedSeq <= prepared.cell.observedSeq {
			cells[key] = prepared.cell
		}
	}
}

func buildCell(projectionUnit Unit, events []session.Event) (unitCell, error) {
	state, err := projectionUnit.InitialState()
	if err != nil {
		return unitCell{}, err
	}
	state, err = validatedRaw(state)
	if err != nil {
		return unitCell{}, fmt.Errorf("initial state: %w", err)
	}
	cell := unitCell{
		state:       state,
		observedSeq: -1,
	}
	advanced, err := advanceCell(projectionUnit, cell, events)
	return advanced.cell, err
}

func advanceCell(
	projectionUnit Unit,
	cell unitCell,
	events []session.Event,
) (cellAdvance, error) {
	stateWasChanged := false
	for _, committed := range events {
		if committed.Seq <= cell.observedSeq {
			continue
		}
		stateChange, transitionErr := applyTransition(projectionUnit, cell.state, committed)
		if transitionErr != nil {
			return cellAdvance{}, fmt.Errorf(
				"apply seq %d: %w",
				committed.Seq,
				transitionErr,
			)
		}
		cell.state = stateChange.State
		cell.observedSeq = committed.Seq
		stateWasChanged = stateWasChanged || stateChange.Changed
	}
	return cellAdvance{
		cell:            cell,
		stateWasChanged: stateWasChanged,
	}, nil
}

func applyTransition(projectionUnit Unit, state json.RawMessage, committed session.Event) (Transition, error) {
	committed = committed.Clone()
	stateChange, err := projectionUnit.ApplyState(cloneRaw(state), committed)
	if err != nil {
		return Transition{}, err
	}
	stateChange.State, err = validatedRaw(stateChange.State)
	if err != nil {
		return Transition{}, fmt.Errorf("next state: %w", err)
	}
	return stateChange, nil
}

func viewValue(projectionUnit Unit, state json.RawMessage) (json.RawMessage, error) {
	view, err := projectionUnit.ViewState(cloneRaw(state))
	if err != nil {
		return nil, err
	}
	return validatedRaw(view)
}

func validatedRaw(rawValue json.RawMessage) (json.RawMessage, error) {
	if err := validateRaw(rawValue); err != nil {
		return nil, err
	}
	return cloneRaw(rawValue), nil
}

func validateRaw(rawValue json.RawMessage) error {
	if len(rawValue) == 0 {
		return errors.New("plain JSON value is absent")
	}
	if err := jsonvalue.Validate(rawValue); err != nil {
		return fmt.Errorf("invalid plain JSON value: %w", err)
	}
	return nil
}

func cloneRaw(rawValue json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), rawValue...)
}

func eventsBefore(events []session.Event, boundary int64) []session.Event {
	for index, committed := range events {
		if committed.Seq >= boundary {
			return events[:index]
		}
	}
	return events
}

func lastSequence(events []session.Event) int64 {
	if len(events) == 0 {
		return -1
	}
	return events[len(events)-1].Seq
}

// ErrHistoryRequired means a Unit cannot advance without the missing log prefix.
var ErrHistoryRequired = errors.New("sessionprojection: committed history is required")

// Advance applies a contiguous event using existing Unit cursors. A missing prefix
// returns ErrHistoryRequired before any Unit is changed.
func (owner *Projection) Advance(committed session.Event) ([]Change, error) {
	if committed.Seq < 0 {
		return nil, errors.New("sessionprojection: invalid event sequence")
	}
	owner.prune()
	for _, entry := range owner.registry.registrations {
		cell, found := owner.cells[entry]
		if (!found && committed.Seq != 0) || (found && cell.observedSeq < committed.Seq-1) {
			return nil, ErrHistoryRequired
		}
	}
	return owner.observe(nil, committed)
}

func validateHistory(events []session.Event) error {
	_, err := validateRestoreEvents(events, 0)
	return err
}

func (owner *Projection) prune() {
	for entry := range owner.cells {
		if owner.registry.registrations[entry.projectionUnit.Key()] != entry {
			delete(owner.cells, entry)
		}
	}
}
