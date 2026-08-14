package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
)

const endSeedEventType = "session/end-seed"

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
	if _, reserved := surfaceEventTypes[canonicalName]; reserved || canonicalName == endSeedEventType {
		panic(fmt.Sprintf("session: event name %q is owned by the core session contract", canonicalName))
	}
	return EventKey[D]{name: canonicalName}
}

func defineSurfaceEvent[D any](canonicalName string) SurfaceEventKey[D] {
	if _, known := surfaceEventTypes[canonicalName]; !known {
		panic(fmt.Sprintf("session: %q is not a core surface event", canonicalName))
	}
	return SurfaceEventKey[D]{name: canonicalName}
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
	return SurfaceOperation{Kind: SurfaceOperationAppend}
}

// SurfaceReplace returns an inclusive positional replacement operation.
func SurfaceReplace(start int64, end int64) SurfaceOperation {
	return SurfaceOperation{Kind: SurfaceOperationReplace, Start: start, End: end}
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
		return json.Marshal(struct {
			Op    string `json:"op"`
			Start int64  `json:"start"`
			End   int64  `json:"end"`
		}{Op: "replace", Start: operation.Start, End: operation.End})
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
	if err := ensureJSONEnd(decoder); err != nil {
		return err
	}
	if replacement.Op != "replace" || !isSafeNonNegative(replacement.Start) || !isSafeNonNegative(replacement.End) {
		return errors.New("session: invalid surface replace operation")
	}
	*operation = SurfaceReplace(replacement.Start, replacement.End)
	return nil
}

// AppendOptions applies to one event admission.
type AppendOptions struct {
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
	return append(json.RawMessage(nil), rawValue...), nil
}

func validateLosslessJSON(rawValue []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(rawValue))
	decoder.UseNumber()
	if err := scanJSONValue(decoder, "$", make(map[string]struct{})); err != nil {
		return err
	}
	return ensureJSONEnd(decoder)
}

func scanJSONValue(decoder *json.Decoder, path string, scratch map[string]struct{}) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if numeric, ok := token.(json.Number); ok {
		parsed, parseErr := numeric.Float64()
		if parseErr != nil || math.IsInf(parsed, 0) || math.IsNaN(parsed) || parsed == 0 && math.Signbit(parsed) {
			return fmt.Errorf("invalid JSON number %q at %s", numeric, path)
		}
		return nil
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return nil
	}
	switch delimiter {
	case '{':
		clear(scratch)
		for decoder.More() {
			nameToken, tokenErr := decoder.Token()
			if tokenErr != nil {
				return tokenErr
			}
			fieldName, ok := nameToken.(string)
			if !ok {
				return fmt.Errorf("object key at %s is not a string", path)
			}
			if _, exists := scratch[fieldName]; exists {
				return fmt.Errorf("duplicate field %q at %s", fieldName, path)
			}
			scratch[fieldName] = struct{}{}
			if err := scanJSONValue(decoder, path+"."+fieldName, make(map[string]struct{})); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	case '[':
		index := 0
		for decoder.More() {
			if err := scanJSONValue(decoder, fmt.Sprintf("%s[%d]", path, index), make(map[string]struct{})); err != nil {
				return err
			}
			index++
		}
		_, err = decoder.Token()
		return err
	default:
		return fmt.Errorf("unexpected delimiter %q", delimiter)
	}
}

func ensureJSONEnd(decoder *json.Decoder) error {
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func cloneEvent(source Event) Event {
	snapshot := source
	snapshot.Data = append(json.RawMessage(nil), source.Data...)
	if source.SourceEventSeqs != nil {
		provenance := append([]int64(nil), (*source.SourceEventSeqs)...)
		snapshot.SourceEventSeqs = &provenance
	}
	if source.SurfaceOp != nil {
		operation := *source.SurfaceOp
		snapshot.SurfaceOp = &operation
	}
	return snapshot
}
