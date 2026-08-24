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
	cells          map[session.Context]unitCell
	refs           int
}

// DriveRegistry is the in-process Registry provider. It stores only derived,
// rebuildable state; durable Session events remain the source of truth.
type DriveRegistry struct {
	plugin.Base
	mu            sync.Mutex
	registrations map[string]*registration
	order         []string
}

// NewDriveRegistry constructs the Session projection Service Plugin.
func NewDriveRegistry() *DriveRegistry {
	return &DriveRegistry{
		registrations: make(map[string]*registration),
	}
}

// Manifest declares the projection Service and Session events it observes.
func (owner *DriveRegistry) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: PluginName,
		Provides: []plugin.ProvidedService{
			plugin.NewProvidedService[Registry](owner),
		},
		Requires: []plugin.ServiceType{
			plugin.ServiceOf[session.LiveStore](),
		},
		Events: []plugin.EventSubscription{
			plugin.EventOf[session.EventAppended](),
			plugin.EventOf[session.Disposed](),
		},
	}
}

// Apply confirms the required Session Store dependency is active.
func (owner *DriveRegistry) Apply(requestContext context.Context) error {
	if err := requestContext.Err(); err != nil {
		return err
	}
	_, err := plugin.Require[session.LiveStore](owner)
	return err
}

// Dispose drops all rebuildable projection state and registrations.
func (owner *DriveRegistry) Dispose(context.Context) error {
	owner.mu.Lock()
	owner.registrations = make(map[string]*registration)
	owner.order = nil
	owner.mu.Unlock()
	return nil
}

// ObserveEvent routes the declared Session event types through one entry point.
func (owner *DriveRegistry) ObserveEvent(
	requestContext context.Context,
	fact plugin.Event,
) error {
	switch observed := fact.(type) {
	case session.EventAppended:
		return owner.observeEvent(
			requestContext,
			observed.Conversation,
			observed.Committed,
		)
	case session.Disposed:
		return owner.observeDisposed(requestContext, observed.Conversation)
	default:
		return nil
	}
}

// Register installs or reference-counts one domain projection definition.
func (owner *DriveRegistry) Register(projectionUnit Unit) (UnitHandle, error) {
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
			cells:          make(map[session.Context]unitCell),
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

	return &unitHandle{
		owner: owner,
		key:   projectionKey,
	}, nil
}

// Snapshot folds missing cells lazily and serves one validated current state.
func (owner *DriveRegistry) Snapshot(conversation session.Context) (Snapshot, error) {
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
func (owner *DriveRegistry) Checkpoint(conversation session.Context) (Checkpoint, error) {
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

func (owner *DriveRegistry) observeEvent(
	requestContext context.Context,
	conversation session.Context,
	committed session.Event,
) error {
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
	owner.mu.Unlock()
	for _, projectionChange := range changes {
		if err := plugin.Publish(
			requestContext,
			owner,
			ProjectionChanged{
				Change: cloneChange(projectionChange),
			},
		); err != nil {
			return err
		}
	}
	return nil
}

func (owner *DriveRegistry) observeDisposed(_ context.Context, conversation session.Context) error {
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
	conversation session.Context,
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

type unitHandle struct {
	once  sync.Once
	owner *DriveRegistry
	key   string
}

func (handleState *unitHandle) Release(context.Context) error {
	if handleState == nil {
		return nil
	}
	handleState.once.Do(func() {
		handleState.owner.unregister(handleState.key)
	})
	return nil
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
