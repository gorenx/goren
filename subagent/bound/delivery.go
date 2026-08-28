package bound

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"

	"github.com/bytedance/sonic"
	"github.com/gorenx/goren/agentmessage"
)

const DeliveryKind = "subagent-delivery"

// Delivery is the durable child-message provenance for one Bound Input. Origin
// retains the producing input source's own provenance without making Bound
// decode it.
type Delivery struct {
	Kind   string                   `json:"kind"`
	Form   agentmessage.ContextForm `json:"form"`
	Input  InputID                  `json:"input"`
	Origin json.RawMessage          `json:"origin"`
}

// NewDelivery snapshots one Input as durable Bound delivery provenance.
func NewDelivery(inputValue Input) (Delivery, error) {
	detached, err := SnapshotInput(inputValue)
	if err != nil {
		return Delivery{}, err
	}
	origin, err := deliveryCodec.Marshal(detached.Source)
	if err != nil {
		return Delivery{}, err
	}
	return snapshotDelivery(
		Delivery{
			Kind:   DeliveryKind,
			Form:   agentmessage.ContextRelay,
			Input:  detached.ID,
			Origin: origin,
		},
	)
}

// SourceKind returns the canonical Bound delivery discriminant.
func (Delivery) SourceKind() string {
	return DeliveryKind
}

// CloneSource validates and detaches the delivery provenance.
func (source Delivery) CloneSource() (agentmessage.MessageSource, error) {
	return snapshotDelivery(source)
}

// MarshalJSON emits the complete validated delivery object.
func (source Delivery) MarshalJSON() ([]byte, error) {
	detached, err := snapshotDelivery(source)
	if err != nil {
		return nil, err
	}
	type deliveryWire Delivery
	return deliveryCodec.Marshal(deliveryWire(detached))
}

// DecodeDelivery restores a delivery from live or replayed message provenance.
func DecodeDelivery(
	origin agentmessage.MessageSource,
) (Delivery, error) {
	if origin == nil || origin.SourceKind() != DeliveryKind {
		return Delivery{}, errors.New(
			"subagent/bound: message source is not a Bound Delivery",
		)
	}
	switch source := origin.(type) {
	case Delivery:
		return snapshotDelivery(source)
	case *Delivery:
		if source == nil {
			return Delivery{}, errors.New(
				"subagent/bound: message source is not a Bound Delivery",
			)
		}
		return snapshotDelivery(*source)
	}
	rawValue, err := deliveryCodec.Marshal(origin)
	if err != nil {
		return Delivery{}, err
	}
	var decoded Delivery
	if err = decodeDeliveryJSON(rawValue, &decoded); err != nil {
		return Delivery{}, err
	}
	return snapshotDelivery(decoded)
}

func snapshotDelivery(source Delivery) (Delivery, error) {
	if source.Input == "" {
		return Delivery{}, errors.New(
			"subagent/bound: Delivery input is empty",
		)
	}
	if source.Form != "" && source.Form != agentmessage.ContextRelay {
		return Delivery{}, errors.New(
			"subagent/bound: Delivery form must be relay",
		)
	}
	origin := bytes.TrimSpace(source.Origin)
	if !json.Valid(origin) || len(origin) == 0 || origin[0] != '{' {
		return Delivery{}, errors.New(
			"subagent/bound: Delivery origin must be a JSON object",
		)
	}
	var header struct {
		Kind string `json:"kind"`
	}
	if err := sonic.Unmarshal(origin, &header); err != nil ||
		header.Kind == "" {
		return Delivery{}, errors.New(
			"subagent/bound: Delivery origin kind is missing",
		)
	}
	return Delivery{
		Kind:   DeliveryKind,
		Form:   agentmessage.ContextRelay,
		Input:  source.Input,
		Origin: append(json.RawMessage(nil), origin...),
	}, nil
}

func decodeDeliveryJSON(rawValue []byte, target any) error {
	decoder := deliveryCodec.NewDecoder(bytes.NewReader(rawValue))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New(
				"subagent/bound: Delivery contains multiple JSON values",
			)
		}
		return err
	}
	return nil
}

var deliveryCodec = sonic.Config{
	EscapeHTML:            true,
	SortMapKeys:           true,
	CompactMarshaler:      true,
	UseUnicodeErrors:      true,
	DisallowUnknownFields: true,
	CopyString:            true,
	ValidateString:        true,
	CaseSensitive:         true,
}.Froze()

var _ agentmessage.MessageSource = Delivery{}
