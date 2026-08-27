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
	conversation session.Context
	boundary     int64
	events       []session.Event
	loaded       bool
}

func (source *eventPrefix) load() []session.Event {
	if !source.loaded {
		source.events = eventsBefore(source.conversation.Events(), source.boundary)
		source.loaded = true
	}
	return source.events
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
	trimmedKey := strings.TrimSpace(projectionKey)
	if trimmedKey == "" || projectionKey != trimmedKey {
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
	candidates, err := owner.prepareCells(conversation, events)
	if err != nil {
		return Snapshot{}, err
	}
	projectionValues := make(Values, len(candidates))
	for _, prepared := range candidates {
		entry := owner.registrations[prepared.key]
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
	owner.installCells(conversation, candidates)
	return Snapshot{
		AsOfSeq: lastSequence(events),
		Values:  projectionValues,
	}, nil
}

// Checkpoint returns detached rebuildable state for every registered unit.
func (owner *DriveRegistry) Checkpoint(conversation session.Context) (Checkpoint, error) {
	if conversation == nil {
		return nil, errors.New("sessionprojection: checkpoint Session is nil")
	}
	events := conversation.Events()
	owner.mu.Lock()
	defer owner.mu.Unlock()
	candidates, err := owner.prepareCells(conversation, events)
	if err != nil {
		return nil, err
	}
	rows := make(Checkpoint, len(candidates))
	for _, prepared := range candidates {
		entry := owner.registrations[prepared.key]
		rows[prepared.key] = CheckpointRow{
			Version: entry.projectionUnit.StateVersion(),
			Seq:     prepared.cell.observedSeq,
			Value:   cloneRaw(prepared.cell.state),
		}
	}
	owner.installCells(conversation, candidates)
	return rows, nil
}

func (owner *DriveRegistry) observeEvent(
	requestContext context.Context,
	conversation session.Context,
	committed session.Event,
) error {
	owner.mu.Lock()
	candidates := make([]cellCandidate, 0, len(owner.registrations))
	changes := make([]Change, 0)
	prefix := eventPrefix{
		conversation: conversation,
		boundary:     committed.Seq,
	}
	for _, projectionKey := range owner.order {
		entry := owner.registrations[projectionKey]
		staged, err := owner.prepareEventCandidate(
			projectionKey,
			conversation,
			committed,
			&prefix,
		)
		if err != nil {
			owner.mu.Unlock()
			return err
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
			owner.mu.Unlock()
			return fmt.Errorf("sessionprojection: projection %q view seq %d: %w", projectionKey, committed.Seq, err)
		}
		changes = append(changes, Change{
			Session: conversation,
			Key:     projectionKey,
			Value:   view,
			Seq:     committed.Seq,
		})
	}
	owner.installCells(conversation, candidates)
	owner.mu.Unlock()
	for _, projectionChange := range changes {
		if err := plugin.Publish(
			requestContext,
			owner,
			Changed{
				Change: projectionChange,
			},
		); err != nil {
			return err
		}
	}
	return nil
}

func (owner *DriveRegistry) prepareEventCandidate(
	projectionKey string,
	conversation session.Context,
	committed session.Event,
	prefix *eventPrefix,
) (*eventCandidate, error) {
	entry := owner.registrations[projectionKey]
	cell, found := entry.cells[conversation]
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

func (owner *DriveRegistry) prepareCells(
	conversation session.Context,
	events []session.Event,
) ([]cellCandidate, error) {
	candidates := make([]cellCandidate, 0, len(owner.registrations))
	for _, projectionKey := range owner.order {
		entry := owner.registrations[projectionKey]
		cell, found := entry.cells[conversation]
		var prepared unitCell
		var err error
		if found {
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

func (owner *DriveRegistry) installCells(
	conversation session.Context,
	candidates []cellCandidate,
) {
	for _, prepared := range candidates {
		owner.registrations[prepared.key].cells[conversation] = prepared.cell
	}
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
