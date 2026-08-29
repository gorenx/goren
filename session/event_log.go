package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/gorenx/goren/agentmessage"
)

// eventLog is one in-memory append-only Session log. It contains no lifecycle,
// Store, persistence,
// transport, model, or Tool execution logic.
type eventLog struct {
	mu sync.RWMutex
	// header is immutable after construction and detached at read boundaries.
	header Header
	// firstLiveSeq separates borrowed seed facts from this live generation.
	firstLiveSeq int64
	// entries is the contiguous append-only Session Event sequence.
	entries []Event
	// view is the Surface derived from entries.
	view surfaceState
	// timeSource supplies timestamps for newly committed Events.
	timeSource TimeSource

	// derived caches model-visible Messages for the current Surface generation.
	derived []agentmessage.Message
	// derivedNodes is the number of current Surface nodes already converted.
	derivedNodes int
	// derivedGeneration invalidates derived after a Surface replacement.
	derivedGeneration uint64
}

// New creates a detached Session and snapshots the borrowed seed.
func New(identifier SessionID, options CreateOptions) (Context, error) {
	return newContextWithClock(identifier, options, systemTimeSource{})
}

func newContextWithClock(
	identifier SessionID,
	options CreateOptions,
	temporalSource TimeSource,
) (*sessionContext, error) {
	sessionLog, err := newWithClock(identifier, options, temporalSource)
	if err != nil {
		return nil, err
	}
	return newSessionContext(sessionLog), nil
}

func newWithClock(
	identifier SessionID,
	options CreateOptions,
	temporalSource TimeSource,
) (*eventLog, error) {
	if temporalSource == nil {
		return nil, errors.New("session: time source is nil")
	}
	headerSnapshot, err := buildHeader(identifier, options.Metadata, temporalSource)
	if err != nil {
		return nil, err
	}
	conversation := &eventLog{
		header:     headerSnapshot,
		timeSource: temporalSource,
	}
	for index, source := range options.Seed {
		candidate := cloneEvent(source)
		if err := validateSeedEvent(candidate, int64(index)); err != nil {
			return nil, fmt.Errorf("session: invalid seed event at index %d: %w", index, err)
		}
		transition, transitionErr := planSurface(
			conversation.view,
			candidate,
			conversation.entries,
			nil,
		)
		if transitionErr != nil {
			return nil, fmt.Errorf("session: invalid seed event at index %d: %w", index, transitionErr)
		}
		conversation.entries = append(conversation.entries, candidate)
		applySurface(&conversation.view, transition)
	}
	conversation.firstLiveSeq = int64(len(conversation.entries))
	if options.Seed != nil &&
		(len(conversation.entries) == 0 ||
			conversation.entries[len(conversation.entries)-1].Type != EndSeedEventName) {
		rawValue, snapshotErr := snapshotPayload(struct{}{})
		if snapshotErr != nil {
			return nil, snapshotErr
		}
		if _, err := conversation.commitBatch([]EventDraft{{
			eventType: EndSeedEventName,
			data:      rawValue,
		}}); err != nil {
			return nil, err
		}
	}
	return conversation, nil
}

// Header returns a detached copy of immutable Session metadata.
func (conversation *eventLog) Header() Header {
	if conversation == nil {
		return Header{}
	}
	conversation.mu.RLock()
	defer conversation.mu.RUnlock()
	return cloneHeader(conversation.header)
}

// ID returns the durable Session identity.
func (conversation *eventLog) ID() SessionID {
	return conversation.Header().ID
}

// FirstLiveSeq returns the constructor seed length before any end-seed marker.
func (conversation *eventLog) FirstLiveSeq() int64 {
	if conversation == nil {
		return 0
	}
	conversation.mu.RLock()
	defer conversation.mu.RUnlock()
	return conversation.firstLiveSeq
}

// Seq returns the next event sequence number.
func (conversation *eventLog) Seq() int64 {
	if conversation == nil {
		return 0
	}
	conversation.mu.RLock()
	defer conversation.mu.RUnlock()
	return int64(len(conversation.entries))
}

// Events returns a detached snapshot. Later appends do not grow the returned slice.
func (conversation *eventLog) Events() []Event {
	if conversation == nil {
		return nil
	}
	conversation.mu.RLock()
	defer conversation.mu.RUnlock()
	detached := make([]Event, len(conversation.entries))
	for index, source := range conversation.entries {
		detached[index] = cloneEvent(source)
	}
	return detached
}

// Surface returns a detached snapshot of the current model-visible sequences.
func (conversation *eventLog) Surface() Surface {
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

// Snapshot returns the append-only log, Surface, and barrier from the same
// committed revision. Consumers that validate positional relationships use it
// instead of composing separate reads.
func (conversation *eventLog) Snapshot() Snapshot {
	if conversation == nil {
		return Snapshot{}
	}
	return conversation.snapshot()
}

func (conversation *eventLog) currentBarrier() WriteBarrier {
	conversation.mu.RLock()
	barrier := WriteBarrier{
		SessionID: conversation.header.ID,
		NextSeq:   int64(len(conversation.entries)),
	}
	conversation.mu.RUnlock()
	return barrier
}

func (conversation *eventLog) snapshot() Snapshot {
	conversation.mu.RLock()
	entries := make([]Event, len(conversation.entries))
	for index, committed := range conversation.entries {
		entries[index] = cloneEvent(committed)
	}
	viewSnapshot := Surface{
		Nodes:             append([]int64(nil), conversation.view.nodes...),
		ReplaceGeneration: conversation.view.replaceGeneration,
	}
	barrier := WriteBarrier{
		SessionID: conversation.header.ID,
		NextSeq:   int64(len(conversation.entries)),
	}
	conversation.mu.RUnlock()
	return Snapshot{
		Events:  entries,
		Surface: viewSnapshot,
		Barrier: barrier,
	}
}

// DeriveMessages projects the current surface into provider-neutral history.
// Non-surface events and empty assistant anchors never enter the result.
func (conversation *eventLog) DeriveMessages() ([]agentmessage.Message, error) {
	if conversation == nil {
		return nil, errors.New("session: derive messages from nil Session")
	}
	conversation.mu.Lock()
	if conversation.derivedGeneration != conversation.view.replaceGeneration {
		conversation.derived = nil
		conversation.derivedNodes = 0
		conversation.derivedGeneration = conversation.view.replaceGeneration
	}
	for conversation.derivedNodes < len(conversation.view.nodes) {
		sequence := conversation.view.nodes[conversation.derivedNodes]
		if sequence < 0 || sequence >= int64(len(conversation.entries)) {
			conversation.mu.Unlock()
			return nil, fmt.Errorf("session: surface node %d is outside the event log", sequence)
		}
		messageValue, err := decodeDerivedMessage(conversation.entries[sequence])
		if err != nil {
			conversation.mu.Unlock()
			return nil, fmt.Errorf("session: derive surface seq %d: %w", sequence, err)
		}
		if messageValue != nil {
			conversation.derived = append(conversation.derived, messageValue)
		}
		conversation.derivedNodes++
	}
	cached := append([]agentmessage.Message(nil), conversation.derived...)
	conversation.mu.Unlock()
	return agentmessage.CloneMessages(cached)
}

func (conversation *eventLog) commitBatch(drafts []EventDraft) ([]Event, error) {
	conversation.mu.Lock()
	defer conversation.mu.Unlock()

	surfaceDrafts := 0
	for _, draft := range drafts {
		if _, eligible := surfaceEventTypes[draft.eventType]; eligible {
			surfaceDrafts++
		}
	}
	multipleSurfaceChanges := surfaceDrafts > 1
	plannedView := conversation.view
	if multipleSurfaceChanges {
		plannedView.nodes = append([]int64(nil), conversation.view.nodes...)
	}
	singleTransition := surfaceTransition{
		appendNode: -1,
	}
	committed := make([]Event, 0, len(drafts))
	for index, draft := range drafts {
		if err := validateEventDraft(draft); err != nil {
			return nil, fmt.Errorf("session: invalid EventDraft at index %d: %w", index, err)
		}
		candidate := Event{
			Type:            draft.eventType,
			Data:            append(json.RawMessage(nil), draft.data...),
			SourceEventSeqs: cloneSequences(draft.sourceEventSeqs),
			Ignorable:       draft.ignorable,
		}
		if draft.surfaceOperation != nil {
			operation := *draft.surfaceOperation
			candidate.SurfaceOp = &operation
		}
		candidate.Seq = int64(len(conversation.entries) + len(committed))
		candidate.Time = conversation.timeSource.CurrentTime().UnixMilli()
		if !isSafeNonNegative(candidate.Seq) || !isSafeInteger(candidate.Time) {
			return nil, errors.New("session: seq and time must be safe integers")
		}
		transition, err := planSurface(
			plannedView,
			candidate,
			conversation.entries,
			committed,
		)
		if err != nil {
			return nil, err
		}
		if multipleSurfaceChanges {
			applySurface(&plannedView, transition)
		} else if transition.appendNode >= 0 {
			singleTransition = transition
		}
		committed = append(committed, candidate)
	}
	conversation.entries = append(conversation.entries, committed...)
	if multipleSurfaceChanges {
		conversation.view = plannedView
	} else {
		applySurface(&conversation.view, singleTransition)
	}
	detached := make([]Event, len(committed))
	for index, entry := range committed {
		detached[index] = cloneEvent(entry)
	}
	return detached, nil
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

func oneEventOptions(options []EventOptions) (EventOptions, error) {
	if len(options) > 1 {
		return EventOptions{}, errors.New("session: NewEventDraft accepts at most one EventOptions value")
	}
	if len(options) == 0 {
		return EventOptions{}, nil
	}
	return options[0], nil
}

func cloneSequences(source *[]int64) *[]int64 {
	if source == nil {
		return nil
	}
	detached := append([]int64(nil), (*source)...)
	return &detached
}

func isSafeInteger(value int64) bool {
	return value >= -maxSafeInteger && value <= maxSafeInteger
}
