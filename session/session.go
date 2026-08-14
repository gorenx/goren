package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

var endSeedEvent = EventKey[struct{}]{name: endSeedEventType}

// Session is an in-memory append-only event log. It contains no persistence,
// transport, model, or Tool execution logic.
type Session struct {
	mu           sync.RWMutex
	header       Header
	firstLiveSeq int64
	entries      []Event
	view         surfaceState
	clock        func() time.Time
	attachment   *storeEntry
}

// New creates a detached Session and snapshots the borrowed seed.
func New(identifier SessionID, options CreateOptions) (*Session, error) {
	return newWithClock(identifier, options, time.Now)
}

func newWithClock(identifier SessionID, options CreateOptions, clock func() time.Time) (*Session, error) {
	if clock == nil {
		return nil, errors.New("session: clock is nil")
	}
	headerSnapshot, err := buildHeader(identifier, options.Metadata, clock)
	if err != nil {
		return nil, err
	}
	conversation := &Session{header: headerSnapshot, clock: clock}
	for index, source := range options.Seed {
		candidate := cloneEvent(source)
		if err := validateSeedEvent(candidate, int64(index)); err != nil {
			return nil, fmt.Errorf("session: invalid seed event at index %d: %w", index, err)
		}
		transition, transitionErr := planSurface(conversation.view, candidate, conversation.entries)
		if transitionErr != nil {
			return nil, fmt.Errorf("session: invalid seed event at index %d: %w", index, transitionErr)
		}
		conversation.entries = append(conversation.entries, candidate)
		applySurface(&conversation.view, transition)
	}
	conversation.firstLiveSeq = int64(len(conversation.entries))
	if len(options.Seed) != 0 && conversation.entries[len(conversation.entries)-1].Type != endSeedEventType {
		if _, err := Append(conversation, endSeedEvent, struct{}{}, AppendOptions{}); err != nil {
			return nil, err
		}
	}
	return conversation, nil
}

// Header returns a detached copy of immutable Session metadata.
func (conversation *Session) Header() Header {
	if conversation == nil {
		return Header{}
	}
	conversation.mu.RLock()
	defer conversation.mu.RUnlock()
	return cloneHeader(conversation.header)
}

// ID returns the durable Session identity.
func (conversation *Session) ID() SessionID {
	return conversation.Header().ID
}

// FirstLiveSeq returns the constructor seed length before any end-seed marker.
func (conversation *Session) FirstLiveSeq() int64 {
	if conversation == nil {
		return 0
	}
	conversation.mu.RLock()
	defer conversation.mu.RUnlock()
	return conversation.firstLiveSeq
}

// Seq returns the next event sequence number.
func (conversation *Session) Seq() int64 {
	if conversation == nil {
		return 0
	}
	conversation.mu.RLock()
	defer conversation.mu.RUnlock()
	return int64(len(conversation.entries))
}

// Events returns a detached snapshot. Later appends do not grow the returned slice.
func (conversation *Session) Events() []Event {
	if conversation == nil {
		return nil
	}
	conversation.mu.RLock()
	defer conversation.mu.RUnlock()
	snapshot := make([]Event, len(conversation.entries))
	for index, source := range conversation.entries {
		snapshot[index] = cloneEvent(source)
	}
	return snapshot
}

// Surface returns a detached snapshot of the current model-visible sequences.
func (conversation *Session) Surface() Surface {
	if conversation == nil {
		return Surface{}
	}
	conversation.mu.RLock()
	defer conversation.mu.RUnlock()
	nodes := make([]int64, len(conversation.view.nodes))
	copy(nodes, conversation.view.nodes)
	return Surface{
		Nodes:             nodes,
		ReplaceGeneration: conversation.view.replaceGeneration,
	}
}

// Append snapshots and commits one typed non-surface event.
func Append[D any](conversation *Session, definition EventKey[D], payload D, options ...AppendOptions) (Event, error) {
	if conversation == nil {
		return Event{}, errors.New("session: append to nil Session")
	}
	if definition.name == "" {
		return Event{}, errors.New("session: append with invalid event key")
	}
	settings, err := oneAppendOptions(options)
	if err != nil {
		return Event{}, err
	}
	rawValue, err := snapshotPayload(payload)
	if err != nil {
		return Event{}, err
	}
	return conversation.appendCandidate(Event{Type: definition.name, Data: rawValue, Ignorable: settings.Ignorable})
}

// AppendSurface snapshots and commits one typed message-producing event.
func AppendSurface[D any](conversation *Session, definition SurfaceEventKey[D], payload D, intent SurfaceIntent) (Event, error) {
	if conversation == nil {
		return Event{}, errors.New("session: append to nil Session")
	}
	if definition.name == "" {
		return Event{}, errors.New("session: append with invalid surface event key")
	}
	rawValue, err := snapshotPayload(payload)
	if err != nil {
		return Event{}, err
	}
	operation := intent.Operation
	provenance := cloneSequences(intent.SourceEventSeqs)
	return conversation.appendCandidate(Event{
		Type: definition.name, Data: rawValue, SourceEventSeqs: provenance,
		SurfaceOp: &operation, Ignorable: intent.Ignorable,
	})
}

func (conversation *Session) appendCandidate(candidate Event) (Event, error) {
	conversation.mu.Lock()
	candidate.Seq = int64(len(conversation.entries))
	candidate.Time = conversation.clock().UnixMilli()
	if !isSafeNonNegative(candidate.Seq) || !isSafeInteger(candidate.Time) {
		conversation.mu.Unlock()
		return Event{}, errors.New("session: seq and time must be safe integers")
	}
	transition, err := planSurface(conversation.view, candidate, conversation.entries)
	if err != nil {
		conversation.mu.Unlock()
		return Event{}, err
	}
	owner := conversation.attachment
	var notify func(Event)
	if owner != nil {
		notify, err = owner.beginAppend()
		if err != nil {
			conversation.mu.Unlock()
			return Event{}, err
		}
	}
	committed := cloneEvent(candidate)
	conversation.entries = append(conversation.entries, committed)
	applySurface(&conversation.view, transition)
	conversation.mu.Unlock()

	if notify != nil {
		notify(committed)
	}
	return cloneEvent(committed), nil
}

func validateSeedEvent(candidate Event, expectedSeq int64) error {
	if candidate.Seq != expectedSeq {
		return fmt.Errorf("seq %d is not contiguous; expected %d", candidate.Seq, expectedSeq)
	}
	if !isSafeNonNegative(candidate.Seq) || !isSafeInteger(candidate.Time) {
		return errors.New("seq and time must be safe integers")
	}
	if len(candidate.Data) == 0 {
		return errors.New("data is absent")
	}
	if err := validateLosslessJSON(candidate.Data); err != nil {
		return fmt.Errorf("data is not lossless JSON: %w", err)
	}
	if candidate.SourceEventSeqs != nil {
		candidate.SourceEventSeqs = cloneSequences(candidate.SourceEventSeqs)
	}
	if candidate.SurfaceOp != nil {
		if _, err := json.Marshal(candidate.SurfaceOp); err != nil {
			return err
		}
	}
	return nil
}

func oneAppendOptions(options []AppendOptions) (AppendOptions, error) {
	if len(options) > 1 {
		return AppendOptions{}, errors.New("session: append accepts at most one AppendOptions value")
	}
	if len(options) == 0 {
		return AppendOptions{}, nil
	}
	return options[0], nil
}

func cloneSequences(source *[]int64) *[]int64 {
	if source == nil {
		return nil
	}
	snapshot := append([]int64(nil), (*source)...)
	return &snapshot
}

func isSafeInteger(value int64) bool {
	return value >= -maxSafeInteger && value <= maxSafeInteger
}
