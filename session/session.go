package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/gorenx/goren/llm"
)

var endSeedEvent = EventKey[struct{}]{name: endSeedEventType}

// Session is an in-memory append-only event log. It contains no persistence,
// transport, model, or Tool execution logic.
type Session struct {
	mu           sync.RWMutex
	producerMu   sync.Mutex
	header       Header
	firstLiveSeq int64
	entries      []Event
	view         surfaceState
	timeSource   TimeSource
	attachment   *storeEntry

	headerFold        *EpochHeader
	headerFoldSeq     int
	contextFold       *RequestRouteContext
	contextFoldSeq    int
	derived           []llm.Message
	derivedNodes      int
	derivedGeneration uint64
}

// New creates a detached Session and snapshots the borrowed seed.
func New(identifier SessionID, options CreateOptions) (*Session, error) {
	return newWithClock(identifier, options, systemTimeSource{})
}

func newWithClock(identifier SessionID, options CreateOptions, temporalSource TimeSource) (*Session, error) {
	if temporalSource == nil {
		return nil, errors.New("session: time source is nil")
	}
	headerSnapshot, err := buildHeader(identifier, options.Metadata, temporalSource)
	if err != nil {
		return nil, err
	}
	conversation := &Session{
		header:     headerSnapshot,
		timeSource: temporalSource,
	}
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

// RequestHeaderValue returns the canonical header in force after the latest
// request/header event. Each committed event is folded at most once.
func (conversation *Session) RequestHeaderValue() (EpochHeader, bool, error) {
	if conversation == nil {
		return EpochHeader{}, false, errors.New("session: request header from nil Session")
	}
	conversation.mu.Lock()
	defer conversation.mu.Unlock()
	for index := conversation.headerFoldSeq; index < len(conversation.entries); index++ {
		entry := conversation.entries[index]
		if entry.Type != RequestHeaderEventName {
			conversation.headerFoldSeq = index + 1
			continue
		}
		var snapshot RequestHeaderSnapshot
		if err := decodeSessionPayload(entry.Data, &snapshot); err != nil {
			return EpochHeader{}, false, fmt.Errorf("session: invalid request/header at seq %d: %w", entry.Seq, err)
		}
		canonical := CanonicalEpochHeader(snapshot.Header)
		conversation.headerFold = &canonical
		conversation.headerFoldSeq = index + 1
	}
	if conversation.headerFold == nil {
		return EpochHeader{}, false, nil
	}
	return CanonicalEpochHeader(*conversation.headerFold), true, nil
}

// RequestContextValue returns the latest resolved provider/model capacity metadata.
func (conversation *Session) RequestContextValue() (RequestRouteContext, bool, error) {
	if conversation == nil {
		return RequestRouteContext{}, false, errors.New("session: request context from nil Session")
	}
	conversation.mu.Lock()
	defer conversation.mu.Unlock()
	for index := conversation.contextFoldSeq; index < len(conversation.entries); index++ {
		entry := conversation.entries[index]
		if entry.Type != RequestContextEventName {
			conversation.contextFoldSeq = index + 1
			continue
		}
		var snapshot RequestRouteContext
		if err := decodeSessionPayload(entry.Data, &snapshot); err != nil {
			return RequestRouteContext{}, false, fmt.Errorf("session: invalid request/context at seq %d: %w", entry.Seq, err)
		}
		if snapshot.Provider == "" || snapshot.Model == "" ||
			(snapshot.ContextWindow != nil && *snapshot.ContextWindow <= 0) {
			return RequestRouteContext{}, false, fmt.Errorf("session: invalid request/context at seq %d", entry.Seq)
		}
		contextSnapshot := snapshot
		contextSnapshot.ContextWindow = cloneInt(snapshot.ContextWindow)
		conversation.contextFold = &contextSnapshot
		conversation.contextFoldSeq = index + 1
	}
	if conversation.contextFold == nil {
		return RequestRouteContext{}, false, nil
	}
	result := *conversation.contextFold
	result.ContextWindow = cloneInt(conversation.contextFold.ContextWindow)
	return result, true, nil
}

// DeriveMessages projects the current surface into provider-neutral history.
// Non-surface events and empty assistant anchors never enter the result.
func (conversation *Session) DeriveMessages() ([]llm.Message, error) {
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
	cached := append([]llm.Message(nil), conversation.derived...)
	conversation.mu.Unlock()
	return llm.CloneMessages(cached)
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

// AppendSerialized commits one non-surface event as an independent runtime
// producer. It preserves the source harness's synchronous append/publication
// boundary while serializing Go goroutines that would be turns of the same
// JavaScript event loop. An OnEvent callback must use Append directly when it
// deliberately verifies the reentry guard, or DeferAfterEvent before appending.
func AppendSerialized[D any](conversation *Session, definition EventKey[D], payload D, options ...AppendOptions) (Event, error) {
	return serializeProducerAppend(conversation, func() (Event, error) {
		return Append(conversation, definition, payload, options...)
	})
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

// AppendSurfaceSerialized is the surface-event counterpart of
// AppendSerialized for independent runtime producers.
func AppendSurfaceSerialized[D any](conversation *Session, definition SurfaceEventKey[D], payload D, intent SurfaceIntent) (Event, error) {
	return serializeProducerAppend(conversation, func() (Event, error) {
		return AppendSurface(conversation, definition, payload, intent)
	})
}

func serializeProducerAppend(conversation *Session, operation func() (Event, error)) (Event, error) {
	if conversation == nil {
		return Event{}, errors.New("session: append to nil Session")
	}
	conversation.producerMu.Lock()
	defer conversation.producerMu.Unlock()
	return operation()
}

func (conversation *Session) appendCandidate(candidate Event) (Event, error) {
	conversation.mu.Lock()
	candidate.Seq = int64(len(conversation.entries))
	candidate.Time = conversation.timeSource.CurrentTime().UnixMilli()
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
	// Append and AppendSurface construct candidate from owner-created snapshots.
	// Keep that owned value as the log entry and detach only at public boundaries.
	committed := candidate
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
