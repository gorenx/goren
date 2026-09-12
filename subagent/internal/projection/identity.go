// Package projection owns Subagent projection units registered with the
// Session projection capability.
package projection

import (
	"bytes"
	"encoding/json"
	"errors"

	"github.com/gorenx/goren/session"
	sessionprojection "github.com/gorenx/goren/session/projection"
	"github.com/gorenx/goren/subagent"
)

const identityKey = "subagent"

var errInvalidIdentity = errors.New("subagent: invalid identity projection")

// Identity is the listing-safe mode and label folded from a Descriptor.
type Identity struct {
	Mode  subagent.Mode `json:"mode"`
	Label *string       `json:"label,omitempty"`
	Seq   int64         `json:"seq"`
}

type identityState struct {
	Identity *Identity `json:"identity,omitempty"`
}

type identityStateWire struct {
	Identity json.RawMessage `json:"identity"`
}

type identityWire struct {
	Mode  subagent.Mode   `json:"mode"`
	Label json.RawMessage `json:"label"`
	Seq   int64           `json:"seq"`
}

// identityUnit folds the latest Descriptor identity. Invalid or unsupported
// Descriptor payloads reset the serializable view to null.
type identityUnit struct{}

func (identityUnit) Key() string {
	return identityKey
}

func (identityUnit) StateVersion() int64 {
	return 2
}

func (identityUnit) InitialState() (json.RawMessage, error) {
	return json.Marshal(identityState{})
}

func (identityUnit) ApplyState(
	state json.RawMessage,
	committed session.Event,
) (sessionprojection.Transition, error) {
	if _, err := decodeIdentityState(state); err != nil {
		return sessionprojection.Transition{}, err
	}
	if committed.Type != subagent.DescriptorEventName {
		return sessionprojection.Transition{
			State:   append(json.RawMessage(nil), state...),
			Changed: false,
		}, nil
	}
	next := identityState{}
	var data subagent.DescriptorData
	if decodeErr := json.Unmarshal(committed.Data, &data); decodeErr == nil {
		switch descriptor := data.DescriptorValue().(type) {
		case subagent.OneShotDescriptor:
			next.Identity = &Identity{
				Mode:  subagent.ModeOneShot,
				Label: cloneString(descriptor.Label),
				Seq:   committed.Seq,
			}
		case subagent.ContinuableDescriptor:
			next.Identity = &Identity{
				Mode:  subagent.ModeContinuable,
				Label: stringPointer(descriptor.Label),
				Seq:   committed.Seq,
			}
		}
	}
	nextState, err := json.Marshal(next)
	if err != nil {
		return sessionprojection.Transition{}, err
	}
	return sessionprojection.Transition{
		State:   nextState,
		Changed: !bytes.Equal(state, nextState),
	}, nil
}

func (identityUnit) ViewState(state json.RawMessage) (json.RawMessage, error) {
	current, err := decodeIdentityState(state)
	if err != nil {
		return nil, err
	}
	if current.Identity == nil {
		return json.RawMessage("null"), nil
	}
	return json.Marshal(current.Identity)
}

// ReadIdentity returns the trustworthy Descriptor identity from one complete
// projection snapshot. Missing and null values mean no child identity exists.
func ReadIdentity(
	values sessionprojection.Values,
) (Identity, bool, error) {
	rawValue, found := values[identityKey]
	if !found {
		return Identity{}, false, nil
	}
	return decodeIdentity(rawValue)
}

func decodeIdentity(rawValue json.RawMessage) (Identity, bool, error) {
	if bytes.Equal(bytes.TrimSpace(rawValue), []byte("null")) {
		return Identity{}, false, nil
	}
	decoded, err := decodeIdentityValue(rawValue)
	if err != nil {
		return Identity{}, false, err
	}
	return decoded, true, nil
}

func decodeIdentityState(rawValue json.RawMessage) (identityState, error) {
	var wire identityStateWire
	if err := decodeJSON(rawValue, &wire); err != nil {
		return identityState{}, err
	}
	if len(wire.Identity) == 0 {
		return identityState{}, nil
	}
	decoded, err := decodeIdentityValue(wire.Identity)
	if err != nil {
		return identityState{}, err
	}
	return identityState{
		Identity: &decoded,
	}, nil
}

func decodeIdentityValue(rawValue json.RawMessage) (Identity, error) {
	var wire identityWire
	if err := decodeJSON(rawValue, &wire); err != nil {
		return Identity{}, err
	}
	if wire.Seq < 0 || wire.Mode == "" {
		return Identity{}, errInvalidIdentity
	}
	decoded := Identity{
		Mode: wire.Mode,
		Seq:  wire.Seq,
	}
	switch decoded.Mode {
	case subagent.ModeOneShot:
		if len(wire.Label) != 0 {
			if bytes.Equal(bytes.TrimSpace(wire.Label), []byte("null")) {
				return Identity{}, errInvalidIdentity
			}
			var label string
			if err := decodeJSON(wire.Label, &label); err != nil {
				return Identity{}, errInvalidIdentity
			}
			decoded.Label = &label
		}
		return decoded, nil
	case subagent.ModeContinuable:
		if len(wire.Label) == 0 ||
			bytes.Equal(bytes.TrimSpace(wire.Label), []byte("null")) {
			return Identity{}, errInvalidIdentity
		}
		var label string
		if err := decodeJSON(wire.Label, &label); err != nil {
			return Identity{}, errInvalidIdentity
		}
		decoded.Label = &label
		return decoded, nil
	default:
		return Identity{}, errInvalidIdentity
	}
}

func cloneString(source *string) *string {
	if source == nil {
		return nil
	}
	return stringPointer(*source)
}

func stringPointer(value string) *string {
	return &value
}

var _ sessionprojection.Unit = identityUnit{}
