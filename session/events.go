package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/gorenx/goren/internal/jsonvalue"
)

// EndSeedEventName separates inherited history from events produced by the
// current live Session generation.
const EndSeedEventName = "session/end-seed"

type eventTypeRegistry struct {
	mutex sync.RWMutex
	// names maps canonical Session Event name keys to registration-presence values.
	names map[string]struct{}
}

var knownEventTypes = eventTypeRegistry{
	names: map[string]struct{}{EndSeedEventName: {}},
}

// surfaceEventTypes maps canonical Event name keys to Surface-eligibility values.
var surfaceEventTypes = map[string]struct{}{
	"user/message":      {},
	"assistant/message": {},
	"tool/result":       {},
}

// EventKey is an owner-defined typed identity for a non-surface Session event.
// Copy the value exported by the event owner instead of reconstructing it by name.
type EventKey[D any] struct {
	name string
}

// SurfaceEventKey is an owner-defined typed identity for an event that can
// enter the ordered model-visible surface.
type SurfaceEventKey[D any] struct {
	name string
}

// DefineEvent declares an extension event. Core message-producing names are
// reserved because only Session's surface contract may define them.
func DefineEvent[D any](canonicalName string) EventKey[D] {
	validateEventName(canonicalName)
	if _, reserved := surfaceEventTypes[canonicalName]; reserved || canonicalName == EndSeedEventName {
		panic(fmt.Sprintf("session: event name %q is owned by the core session contract", canonicalName))
	}
	registerKnownEventType(canonicalName)
	return EventKey[D]{name: canonicalName}
}

func defineSurfaceEvent[D any](canonicalName string) SurfaceEventKey[D] {
	if _, known := surfaceEventTypes[canonicalName]; !known {
		panic(fmt.Sprintf("session: %q is not a core surface event", canonicalName))
	}
	registerKnownEventType(canonicalName)
	return SurfaceEventKey[D]{name: canonicalName}
}

// IsKnownEventType reports whether this build registered an event definition
// for the canonical name. Persistence uses it to refuse unknown required
// events while allowing explicitly ignorable extension records.
func IsKnownEventType(canonicalName string) bool {
	return knownEventTypes.contains(canonicalName)
}

func registerKnownEventType(canonicalName string) {
	knownEventTypes.register(canonicalName)
}

func (owner *eventTypeRegistry) contains(canonicalName string) bool {
	owner.mutex.RLock()
	_, found := owner.names[canonicalName]
	owner.mutex.RUnlock()
	return found
}

func (owner *eventTypeRegistry) register(canonicalName string) {
	owner.mutex.Lock()
	owner.names[canonicalName] = struct{}{}
	owner.mutex.Unlock()
}

func validateEventName(canonicalName string) {
	if strings.TrimSpace(canonicalName) == "" || canonicalName != strings.TrimSpace(canonicalName) {
		panic("session: event name must be non-empty and trimmed")
	}
}

// SurfaceOperationKind identifies how a message-producing event changes the surface.
type SurfaceOperationKind string

const (
	// SurfaceOperationAppend adds the event to the surface tail.
	SurfaceOperationAppend SurfaceOperationKind = "append"
	// SurfaceOperationReplace shadows an inclusive range with the new event.
	SurfaceOperationReplace SurfaceOperationKind = "replace"
)

// SurfaceOperation is the Go representation of the source `"append" | {op,
// start,end}` union. Fields Start and End are meaningful only for replace.
type SurfaceOperation struct {
	Kind  SurfaceOperationKind
	Start int64
	End   int64
}

// SurfaceAppend returns the normal tail-append operation.
func SurfaceAppend() SurfaceOperation {
	return SurfaceOperation{
		Kind: SurfaceOperationAppend,
	}
}

// SurfaceReplace returns an inclusive positional replacement operation.
func SurfaceReplace(start int64, end int64) SurfaceOperation {
	return SurfaceOperation{
		Kind:  SurfaceOperationReplace,
		Start: start,
		End:   end,
	}
}

// MarshalJSON preserves the pinned TypeScript surfaceOp union on the wire.
func (operation SurfaceOperation) MarshalJSON() ([]byte, error) {
	switch operation.Kind {
	case SurfaceOperationAppend:
		return json.Marshal("append")
	case SurfaceOperationReplace:
		if !isSafeNonNegative(operation.Start) || !isSafeNonNegative(operation.End) {
			return nil, errors.New("session: surface replace bounds must be non-negative safe integers")
		}
		return json.Marshal(
			struct {
				Op    string `json:"op"`
				Start int64  `json:"start"`
				End   int64  `json:"end"`
			}{
				Op:    "replace",
				Start: operation.Start,
				End:   operation.End,
			},
		)
	default:
		return nil, fmt.Errorf("session: unsupported surface operation %q", operation.Kind)
	}
}

// UnmarshalJSON accepts only the exact pinned TypeScript surfaceOp variants.
func (operation *SurfaceOperation) UnmarshalJSON(rawValue []byte) error {
	if operation == nil {
		return errors.New("session: cannot decode surface operation into nil target")
	}
	if bytes.Equal(bytes.TrimSpace(rawValue), []byte(`"append"`)) {
		*operation = SurfaceAppend()
		return nil
	}
	if err := jsonvalue.Validate(rawValue); err != nil {
		return fmt.Errorf("session: invalid surface operation: %w", err)
	}
	var replacement struct {
		Op    string `json:"op"`
		Start int64  `json:"start"`
		End   int64  `json:"end"`
	}
	decoder := json.NewDecoder(bytes.NewReader(rawValue))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&replacement); err != nil {
		return fmt.Errorf("session: invalid surface operation: %w", err)
	}
	if replacement.Op != "replace" || !isSafeNonNegative(replacement.Start) || !isSafeNonNegative(replacement.End) {
		return errors.New("session: invalid surface replace operation")
	}
	*operation = SurfaceReplace(replacement.Start, replacement.End)
	return nil
}

// EventOptions controls one non-surface Event envelope.
type EventOptions struct {
	// Ignorable marks an informational event an older reader may safely skip.
	Ignorable bool
}

// SurfaceIntent is required for message-producing events and forbidden on all others.
type SurfaceIntent struct {
	Operation       SurfaceOperation
	SourceEventSeqs *[]int64
	Ignorable       bool
}

// Event is one immutable entry in a Session log. Accessors return detached
// copies so Data and provenance cannot mutate committed history.
type Event struct {
	Type            string            `json:"type"`
	Seq             int64             `json:"seq"`
	Time            int64             `json:"time"`
	Data            json.RawMessage   `json:"data"`
	SourceEventSeqs *[]int64          `json:"sourceEventSeqs,omitempty"`
	SurfaceOp       *SurfaceOperation `json:"surfaceOp,omitempty"`
	Ignorable       bool              `json:"ignorable,omitempty"`
}

func snapshotPayload[D any](payload D) (json.RawMessage, error) {
	rawValue, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("session: event data is not JSON-serializable: %w", err)
	}
	if err := validateLosslessJSON(rawValue); err != nil {
		return nil, fmt.Errorf("session: event data is not lossless JSON: %w", err)
	}
	return rawValue, nil
}

func validateLosslessJSON(rawValue []byte) error {
	return jsonvalue.Validate(rawValue)
}

func cloneEvent(source Event) Event {
	detached := source
	detached.Data = append(json.RawMessage(nil), source.Data...)
	if source.SourceEventSeqs != nil {
		provenance := append([]int64(nil), (*source.SourceEventSeqs)...)
		detached.SourceEventSeqs = &provenance
	}
	if source.SurfaceOp != nil {
		operation := *source.SurfaceOp
		detached.SurfaceOp = &operation
	}
	return detached
}
