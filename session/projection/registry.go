package projection

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/gorenx/goren/internal/jsonvalue"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
)

type unitCell struct {
	state       json.RawMessage
	observedSeq int64
}

type registration struct {
	projectionUnit Unit
	cells          map[*session.Session]unitCell
	refs           int
}

// DriveRegistry is the in-process Registry provider. It stores only derived,
// rebuildable state; durable Session events remain the source of truth.
type DriveRegistry struct {
	mu            sync.Mutex
	registrations map[string]*registration
	order         []string
	listeners     map[uint64]ChangeListener
	nextListener  uint64
}

// NewDriveRegistry subscribes once to Session commits and disposal.
func NewDriveRegistry(pluginScope *plugin.Scope) (*DriveRegistry, error) {
	if pluginScope == nil {
		return nil, errors.New("sessionprojection: plugin Scope is nil")
	}
	owner := &DriveRegistry{
		registrations: make(map[string]*registration),
		listeners:     make(map[uint64]ChangeListener),
	}
	if _, err := session.OnEvent(pluginScope, owner.observeEvent); err != nil {
		return nil, err
	}
	if _, err := session.OnDisposed(pluginScope, owner.observeDisposed); err != nil {
		return nil, err
	}
	return owner, nil
}

// Register installs or reference-counts one domain projection definition.
func (owner *DriveRegistry) Register(ownerScope *plugin.Scope, projectionUnit Unit) (plugin.Disposer, error) {
	if ownerScope == nil {
		return nil, errors.New("sessionprojection: registration Scope is nil")
	}
	if projectionUnit == nil {
		return nil, errors.New("sessionprojection: projection Unit is nil")
	}
	projectionKey := projectionUnit.Key()
	if strings.TrimSpace(projectionKey) == "" || projectionKey != strings.TrimSpace(projectionKey) {
		return nil, errors.New("sessionprojection: projection key must be non-empty and trimmed")
	}
	version := projectionUnit.StateVersion()
	if version < 0 {
		return nil, fmt.Errorf("sessionprojection: projection %q stateVersion must be a non-negative integer", projectionKey)
	}

	owner.mu.Lock()
	existing := owner.registrations[projectionKey]
	if existing == nil {
		owner.registrations[projectionKey] = &registration{
			projectionUnit: projectionUnit,
			cells:          make(map[*session.Session]unitCell),
			refs:           1,
		}
		owner.order = append(owner.order, projectionKey)
	} else if existing.projectionUnit.StateVersion() != version {
		owner.mu.Unlock()
		return nil, fmt.Errorf(
			"sessionprojection: key %q is already registered at stateVersion %d; refusing stateVersion %d",
			projectionKey, existing.projectionUnit.StateVersion(), version,
		)
	} else {
		existing.refs++
	}
	owner.mu.Unlock()

	release, err := plugin.Own(ownerScope, "sessionProjections.register("+projectionKey+")", func(context.Context) error {
		owner.unregister(projectionKey)
		return nil
	})
	if err != nil {
		owner.unregister(projectionKey)
		return nil, err
	}
	return release, nil
}

// OnChanged registers a scope-owned whole-value change consumer.
func (owner *DriveRegistry) OnChanged(ownerScope *plugin.Scope, listener ChangeListener) (plugin.Disposer, error) {
	if ownerScope == nil {
		return nil, errors.New("sessionprojection: listener Scope is nil")
	}
	if listener == nil {
		return nil, errors.New("sessionprojection: ChangeListener is nil")
	}
	owner.mu.Lock()
	owner.nextListener++
	identifier := owner.nextListener
	owner.listeners[identifier] = listener
	owner.mu.Unlock()

	release, err := plugin.Own(ownerScope, "sessionProjections.onChanged()", func(context.Context) error {
		owner.mu.Lock()
		delete(owner.listeners, identifier)
		owner.mu.Unlock()
		return nil
	})
	if err != nil {
		owner.mu.Lock()
		delete(owner.listeners, identifier)
		owner.mu.Unlock()
		return nil, err
	}
	return release, nil
}

// Snapshot folds missing cells lazily and serves one validated current cut.
func (owner *DriveRegistry) Snapshot(conversation *session.Session) (Snapshot, error) {
	if conversation == nil {
		return Snapshot{}, errors.New("sessionprojection: snapshot Session is nil")
	}
	events := conversation.Events()
	owner.mu.Lock()
	defer owner.mu.Unlock()
	projectionValues := make(Values, len(owner.registrations))
	for _, projectionKey := range owner.order {
		entry := owner.registrations[projectionKey]
		if entry == nil {
			continue
		}
		cell, err := owner.cellFor(entry, conversation, events)
		if err != nil {
			return Snapshot{}, err
		}
		view, err := viewValue(entry.projectionUnit, cell.state)
		if err != nil {
			return Snapshot{}, fmt.Errorf("sessionprojection: projection %q view: %w", projectionKey, err)
		}
		projectionValues[projectionKey] = view
	}
	return Snapshot{AsOfSeq: lastSequence(events), Values: projectionValues}, nil
}

// Checkpoint returns detached rebuildable state for every registered unit.
func (owner *DriveRegistry) Checkpoint(conversation *session.Session) (Checkpoint, error) {
	if conversation == nil {
		return nil, errors.New("sessionprojection: checkpoint Session is nil")
	}
	events := conversation.Events()
	owner.mu.Lock()
	defer owner.mu.Unlock()
	rows := make(Checkpoint, len(owner.registrations))
	for _, projectionKey := range owner.order {
		entry := owner.registrations[projectionKey]
		if entry == nil {
			continue
		}
		cell, err := owner.cellFor(entry, conversation, events)
		if err != nil {
			return nil, err
		}
		rows[projectionKey] = CheckpointRow{
			Version: entry.projectionUnit.StateVersion(),
			Seq:     cell.observedSeq,
			Value:   cloneRaw(cell.state),
		}
	}
	return rows, nil
}

func (owner *DriveRegistry) observeEvent(_ context.Context, conversation *session.Session, committed session.Event) error {
	owner.mu.Lock()
	changes := make([]Change, 0)
	for _, projectionKey := range owner.order {
		entry := owner.registrations[projectionKey]
		if entry == nil {
			continue
		}
		cell, found := entry.cells[conversation]
		if !found {
			prefix := eventsBefore(conversation.Events(), committed.Seq)
			built, err := buildCell(entry.projectionUnit, prefix)
			if err != nil {
				owner.mu.Unlock()
				return fmt.Errorf("sessionprojection: projection %q late fold: %w", projectionKey, err)
			}
			cell = built
		}
		if cell.observedSeq >= committed.Seq {
			continue
		}
		stateChange, err := applyTransition(entry.projectionUnit, cell.state, committed)
		if err != nil {
			owner.mu.Unlock()
			return fmt.Errorf("sessionprojection: projection %q apply seq %d: %w", projectionKey, committed.Seq, err)
		}
		cell.state = stateChange.State
		cell.observedSeq = committed.Seq
		entry.cells[conversation] = cell
		if !stateChange.Changed {
			continue
		}
		view, err := viewValue(entry.projectionUnit, cell.state)
		if err != nil {
			owner.mu.Unlock()
			return fmt.Errorf("sessionprojection: projection %q view seq %d: %w", projectionKey, committed.Seq, err)
		}
		changes = append(changes, Change{
			Session: conversation, Key: projectionKey, Value: view, Seq: committed.Seq,
		})
	}
	listeners := owner.listenerSnapshotLocked()
	owner.mu.Unlock()
	for _, projectionChange := range changes {
		for _, listener := range listeners {
			listener.ProjectionChanged(cloneChange(projectionChange))
		}
	}
	return nil
}

func (owner *DriveRegistry) observeDisposed(_ context.Context, conversation *session.Session) error {
	owner.mu.Lock()
	for _, entry := range owner.registrations {
		delete(entry.cells, conversation)
	}
	owner.mu.Unlock()
	return nil
}

func (owner *DriveRegistry) unregister(projectionKey string) {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	entry := owner.registrations[projectionKey]
	if entry == nil {
		return
	}
	entry.refs--
	if entry.refs != 0 {
		return
	}
	delete(owner.registrations, projectionKey)
	for index, candidate := range owner.order {
		if candidate == projectionKey {
			owner.order = append(owner.order[:index], owner.order[index+1:]...)
			break
		}
	}
}

func (owner *DriveRegistry) cellFor(
	entry *registration,
	conversation *session.Session,
	events []session.Event,
) (unitCell, error) {
	cell, found := entry.cells[conversation]
	if !found {
		built, err := buildCell(entry.projectionUnit, events)
		if err != nil {
			return unitCell{}, fmt.Errorf("sessionprojection: projection %q fold: %w", entry.projectionUnit.Key(), err)
		}
		entry.cells[conversation] = built
		return built, nil
	}
	for _, committed := range events {
		if committed.Seq <= cell.observedSeq {
			continue
		}
		stateChange, err := applyTransition(entry.projectionUnit, cell.state, committed)
		if err != nil {
			return unitCell{}, fmt.Errorf(
				"sessionprojection: projection %q fold seq %d: %w",
				entry.projectionUnit.Key(), committed.Seq, err,
			)
		}
		cell.state = stateChange.State
		cell.observedSeq = committed.Seq
	}
	entry.cells[conversation] = cell
	return cell, nil
}

func (owner *DriveRegistry) listenerSnapshotLocked() []ChangeListener {
	listeners := make([]ChangeListener, 0, len(owner.listeners))
	for identifier := uint64(1); identifier <= owner.nextListener; identifier++ {
		if listener := owner.listeners[identifier]; listener != nil {
			listeners = append(listeners, listener)
		}
	}
	return listeners
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
	cell := unitCell{state: state, observedSeq: -1}
	for _, committed := range events {
		stateChange, transitionErr := applyTransition(projectionUnit, cell.state, committed)
		if transitionErr != nil {
			return unitCell{}, fmt.Errorf("apply seq %d: %w", committed.Seq, transitionErr)
		}
		cell.state = stateChange.State
		cell.observedSeq = committed.Seq
	}
	return cell, nil
}

func applyTransition(projectionUnit Unit, state json.RawMessage, committed session.Event) (Transition, error) {
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
	if len(rawValue) == 0 {
		return nil, errors.New("plain JSON value is absent")
	}
	if err := jsonvalue.Validate(rawValue); err != nil {
		return nil, fmt.Errorf("invalid plain JSON value: %w", err)
	}
	return cloneRaw(rawValue), nil
}

func cloneRaw(rawValue json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), rawValue...)
}

func cloneChange(source Change) Change {
	result := source
	result.Value = cloneRaw(source.Value)
	return result
}

func eventsBefore(events []session.Event, boundary int64) []session.Event {
	prefix := make([]session.Event, 0, len(events))
	for _, committed := range events {
		if committed.Seq >= boundary {
			break
		}
		prefix = append(prefix, committed)
	}
	return prefix
}

func lastSequence(events []session.Event) int64 {
	if len(events) == 0 {
		return -1
	}
	return events[len(events)-1].Seq
}
