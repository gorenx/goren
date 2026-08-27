package subagent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gorenx/goren/internal/jsonvalue"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/tools"
)

// DescriptorData is the typed Session-event envelope for the Descriptor sum.
type DescriptorData struct {
	value Descriptor
}

// DescriptorValue returns the detached domain variant when decoding succeeded.
func (data DescriptorData) DescriptorValue() Descriptor {
	return cloneDescriptor(data.value)
}

// MarshalJSON encodes one strictly validated current-version variant.
func (data DescriptorData) MarshalJSON() ([]byte, error) {
	switch identity := data.value.(type) {
	case OneShotDescriptor:
		if identity.Version != DescriptorVersion || identity.Mode != ModeOneShot {
			return nil, errors.New("subagent: invalid one-shot descriptor identity")
		}
		return json.Marshal(struct {
			Version  int     `json:"version"`
			Mode     Mode    `json:"mode"`
			Provider string  `json:"provider"`
			Label    *string `json:"label,omitempty"`
		}{
			Version:  DescriptorVersion,
			Mode:     ModeOneShot,
			Provider: identity.Provider,
			Label:    cloneString(identity.Label),
		})
	case ContinuableDescriptor:
		if identity.Version != DescriptorVersion || identity.Mode != ModeContinuable {
			return nil, errors.New("subagent: invalid continuable descriptor identity")
		}
		wireFilter, filterErr := encodeToolRestriction(identity.ToolFilter)
		if filterErr != nil {
			return nil, filterErr
		}
		return json.Marshal(struct {
			Version       int                    `json:"version"`
			Mode          Mode                   `json:"mode"`
			Provider      string                 `json:"provider"`
			Label         string                 `json:"label"`
			AgentProvider *string                `json:"agentProvider,omitempty"`
			AgentModel    *string                `json:"agentModel,omitempty"`
			Persona       *string                `json:"persona,omitempty"`
			ToolFilter    *toolRestrictionRecord `json:"toolFilter,omitempty"`
		}{
			Version:       DescriptorVersion,
			Mode:          ModeContinuable,
			Provider:      identity.Provider,
			Label:         identity.Label,
			AgentProvider: cloneString(identity.AgentProvider),
			AgentModel:    cloneString(identity.AgentModel),
			Persona:       cloneString(identity.Persona),
			ToolFilter:    wireFilter,
		})
	case BoundDescriptor:
		if identity.Version != DescriptorVersion || identity.Mode != ModeBound {
			return nil, errors.New("subagent: invalid Bound descriptor identity")
		}
		return json.Marshal(struct {
			Version  int    `json:"version"`
			Mode     Mode   `json:"mode"`
			Provider string `json:"provider"`
			Label    string `json:"label"`
		}{
			Version:  DescriptorVersion,
			Mode:     ModeBound,
			Provider: identity.Provider,
			Label:    identity.Label,
		})
	case nil:
		return nil, errors.New("subagent: descriptor value is missing")
	default:
		return nil, fmt.Errorf(
			"subagent: unsupported descriptor variant %T",
			identity,
		)
	}
}

// UnmarshalJSON rejects unknown fields and malformed current-version data
// while preserving unsupported-version classification.
func (data *DescriptorData) UnmarshalJSON(rawValue []byte) error {
	if data == nil {
		return errors.New("subagent: cannot decode descriptor into nil target")
	}
	if err := jsonvalue.Validate(rawValue); err != nil {
		return fmt.Errorf("subagent: invalid persisted descriptor JSON: %w", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(rawValue, &fields); err != nil || fields == nil {
		return errors.New("subagent: persisted descriptor payload must be an object")
	}
	versionValue, err := decodeDescriptorVersion(fields["version"])
	if err != nil {
		return err
	}
	if versionValue != float64(DescriptorVersion) {
		data.value = nil
		return nil
	}
	modeValue, err := requiredString(fields, "mode")
	if err != nil {
		return err
	}
	switch Mode(modeValue) {
	case ModeOneShot:
		identity, decodeErr := decodeOneShotDescriptor(fields)
		if decodeErr != nil {
			return decodeErr
		}
		data.value = identity
		return nil
	case ModeContinuable:
		identity, decodeErr := decodeContinuableDescriptor(fields)
		if decodeErr != nil {
			return decodeErr
		}
		data.value = identity
		return nil
	case ModeBound:
		identity, decodeErr := decodeBoundDescriptor(fields)
		if decodeErr != nil {
			return decodeErr
		}
		data.value = identity
		return nil
	default:
		return errors.New(
			"subagent: persisted descriptor mode must be \"one-shot\", \"continuable\", or \"bound\"",
		)
	}
}

// SnapshotDescriptor validates and detaches one descriptor before SeedBuilder
// work or child creation begins.
func SnapshotDescriptor(source Descriptor) (DescriptorData, error) {
	var normalized Descriptor
	switch identity := source.(type) {
	case OneShotDescriptor:
		normalized = OneShotDescriptor{
			Version:  DescriptorVersion,
			Mode:     ModeOneShot,
			Provider: identity.Provider,
			Label:    cloneString(identity.Label),
		}
	case ContinuableDescriptor:
		filterValue, filterErr := cloneToolRestriction(identity.ToolFilter)
		if filterErr != nil {
			return DescriptorData{}, filterErr
		}
		normalized = ContinuableDescriptor{
			Version:       DescriptorVersion,
			Mode:          ModeContinuable,
			Provider:      identity.Provider,
			Label:         identity.Label,
			AgentProvider: cloneString(identity.AgentProvider),
			AgentModel:    cloneString(identity.AgentModel),
			Persona:       cloneString(identity.Persona),
			ToolFilter:    filterValue,
		}
	case BoundDescriptor:
		normalized = BoundDescriptor{
			Version:  DescriptorVersion,
			Mode:     ModeBound,
			Provider: identity.Provider,
			Label:    identity.Label,
		}
	case nil:
		return DescriptorData{}, errors.New("subagent: descriptor is required")
	default:
		return DescriptorData{}, fmt.Errorf(
			"subagent: unsupported descriptor variant %T",
			identity,
		)
	}
	candidate := DescriptorData{
		value: normalized,
	}
	rawValue, err := candidate.MarshalJSON()
	if err != nil {
		return DescriptorData{}, err
	}
	var detached DescriptorData
	if err = json.Unmarshal(rawValue, &detached); err != nil {
		return DescriptorData{}, err
	}
	return detached, nil
}

// FoldDescriptor returns the first authoritative supported descriptor in
// one child log.
func FoldDescriptor(events []session.Event) (Descriptor, bool, error) {
	for _, committed := range events {
		if committed.Type != DescriptorEventName {
			continue
		}
		var data DescriptorData
		if err := json.Unmarshal(committed.Data, &data); err != nil {
			return nil, false, fmt.Errorf(
				"subagent: descriptor at seq %d: %w",
				committed.Seq,
				err,
			)
		}
		if data.value == nil {
			return nil, false, nil
		}
		return data.DescriptorValue(), true, nil
	}
	return nil, false, nil
}

type toolRestrictionRecord struct {
	Allow *[]string `json:"allow,omitempty"`
	Deny  *[]string `json:"deny,omitempty"`
}

func decodeDescriptorVersion(rawValue json.RawMessage) (float64, error) {
	if len(rawValue) == 0 {
		return 0, errors.New("subagent: persisted descriptor version must be a number")
	}
	decoder := json.NewDecoder(bytes.NewReader(rawValue))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return 0, errors.New("subagent: persisted descriptor version must be a number")
	}
	numeric, matches := decoded.(json.Number)
	if !matches {
		return 0, errors.New("subagent: persisted descriptor version must be a number")
	}
	parsed, err := numeric.Float64()
	if err != nil {
		return 0, errors.New("subagent: persisted descriptor version must be a number")
	}
	return parsed, nil
}

func decodeOneShotDescriptor(
	fields map[string]json.RawMessage,
) (OneShotDescriptor, error) {
	if err := rejectUnknownFields(fields, map[string]struct{}{
		"version":  {},
		"mode":     {},
		"provider": {},
		"label":    {},
	}); err != nil {
		return OneShotDescriptor{}, err
	}
	establishedName, err := requiredString(fields, "provider")
	if err != nil {
		return OneShotDescriptor{}, err
	}
	labelValue, err := optionalString(fields, "label")
	if err != nil {
		return OneShotDescriptor{}, err
	}
	return OneShotDescriptor{
		Version:  DescriptorVersion,
		Mode:     ModeOneShot,
		Provider: establishedName,
		Label:    labelValue,
	}, nil
}

func decodeContinuableDescriptor(
	fields map[string]json.RawMessage,
) (ContinuableDescriptor, error) {
	if err := rejectUnknownFields(fields, map[string]struct{}{
		"version":       {},
		"mode":          {},
		"provider":      {},
		"label":         {},
		"agentProvider": {},
		"agentModel":    {},
		"persona":       {},
		"toolFilter":    {},
	}); err != nil {
		return ContinuableDescriptor{}, err
	}
	establishedName, err := requiredString(fields, "provider")
	if err != nil {
		return ContinuableDescriptor{}, err
	}
	labelValue, err := requiredString(fields, "label")
	if err != nil {
		return ContinuableDescriptor{}, err
	}
	agentProvider, err := optionalString(fields, "agentProvider")
	if err != nil {
		return ContinuableDescriptor{}, err
	}
	agentModel, err := optionalString(fields, "agentModel")
	if err != nil {
		return ContinuableDescriptor{}, err
	}
	personaValue, err := optionalString(fields, "persona")
	if err != nil {
		return ContinuableDescriptor{}, err
	}
	filterValue, err := decodeToolRestriction(fields)
	if err != nil {
		return ContinuableDescriptor{}, err
	}
	return ContinuableDescriptor{
		Version:       DescriptorVersion,
		Mode:          ModeContinuable,
		Provider:      establishedName,
		Label:         labelValue,
		AgentProvider: agentProvider,
		AgentModel:    agentModel,
		Persona:       personaValue,
		ToolFilter:    filterValue,
	}, nil
}

func decodeBoundDescriptor(
	fields map[string]json.RawMessage,
) (BoundDescriptor, error) {
	if err := rejectUnknownFields(fields, map[string]struct{}{
		"version":  {},
		"mode":     {},
		"provider": {},
		"label":    {},
	}); err != nil {
		return BoundDescriptor{}, err
	}
	establishedName, err := requiredString(fields, "provider")
	if err != nil {
		return BoundDescriptor{}, err
	}
	labelValue, err := requiredString(fields, "label")
	if err != nil {
		return BoundDescriptor{}, err
	}
	return BoundDescriptor{
		Version:  DescriptorVersion,
		Mode:     ModeBound,
		Provider: establishedName,
		Label:    labelValue,
	}, nil
}

func rejectUnknownFields(
	fields map[string]json.RawMessage,
	known map[string]struct{},
) error {
	for fieldName := range fields {
		if _, accepted := known[fieldName]; !accepted {
			return fmt.Errorf(
				"subagent: persisted descriptor has unknown field %q",
				fieldName,
			)
		}
	}
	return nil
}

func requiredString(
	fields map[string]json.RawMessage,
	fieldName string,
) (string, error) {
	rawValue, found := fields[fieldName]
	if !found {
		return "", fmt.Errorf(
			"subagent: persisted descriptor %s must be a string",
			fieldName,
		)
	}
	trimmed := bytes.TrimSpace(rawValue)
	if len(trimmed) < 2 || trimmed[0] != '"' {
		return "", fmt.Errorf(
			"subagent: persisted descriptor %s must be a string",
			fieldName,
		)
	}
	var decoded string
	if err := json.Unmarshal(rawValue, &decoded); err != nil {
		return "", fmt.Errorf(
			"subagent: persisted descriptor %s must be a string",
			fieldName,
		)
	}
	return decoded, nil
}

func optionalString(
	fields map[string]json.RawMessage,
	fieldName string,
) (*string, error) {
	rawValue, found := fields[fieldName]
	if !found {
		return nil, nil
	}
	trimmed := bytes.TrimSpace(rawValue)
	if len(trimmed) < 2 || trimmed[0] != '"' {
		return nil, fmt.Errorf(
			"subagent: persisted descriptor %s must be a string",
			fieldName,
		)
	}
	var decoded string
	if err := json.Unmarshal(rawValue, &decoded); err != nil {
		return nil, fmt.Errorf(
			"subagent: persisted descriptor %s must be a string",
			fieldName,
		)
	}
	return &decoded, nil
}

func decodeToolRestriction(
	fields map[string]json.RawMessage,
) (*tools.ToolRestriction, error) {
	rawValue, found := fields["toolFilter"]
	if !found {
		return nil, nil
	}
	var record map[string]json.RawMessage
	if err := json.Unmarshal(rawValue, &record); err != nil || record == nil {
		return nil, errors.New(
			"subagent: persisted descriptor toolFilter must be an object",
		)
	}
	if err := rejectToolRestrictionFields(record); err != nil {
		return nil, err
	}
	allowValues, allowFound, err := optionalStringSlice(record, "allow")
	if err != nil {
		return nil, err
	}
	denyValues, denyFound, err := optionalStringSlice(record, "deny")
	if err != nil {
		return nil, err
	}
	if !allowFound && !denyFound {
		return nil, errors.New(
			"subagent: persisted descriptor toolFilter must declare allow and/or deny",
		)
	}
	return &tools.ToolRestriction{
		Allow: allowValues,
		Deny:  denyValues,
	}, nil
}

func rejectToolRestrictionFields(fields map[string]json.RawMessage) error {
	for fieldName := range fields {
		if fieldName != "allow" && fieldName != "deny" {
			return fmt.Errorf(
				"subagent: persisted descriptor toolFilter has unknown field %q",
				fieldName,
			)
		}
	}
	return nil
}

func optionalStringSlice(
	fields map[string]json.RawMessage,
	fieldName string,
) ([]string, bool, error) {
	rawValue, found := fields[fieldName]
	if !found {
		return nil, false, nil
	}
	var decoded []string
	if err := json.Unmarshal(rawValue, &decoded); err != nil || decoded == nil {
		return nil, false, fmt.Errorf(
			"subagent: persisted descriptor toolFilter.%s must be an array of strings",
			fieldName,
		)
	}
	return decoded, true, nil
}

func encodeToolRestriction(
	filterValue *tools.ToolRestriction,
) (*toolRestrictionRecord, error) {
	if filterValue == nil {
		return nil, nil
	}
	if filterValue.Allow == nil && filterValue.Deny == nil {
		return nil, errors.New(
			"subagent: descriptor toolFilter must declare allow and/or deny",
		)
	}
	wireValue := &toolRestrictionRecord{}
	if filterValue.Allow != nil {
		allowValues := cloneStrings(filterValue.Allow)
		wireValue.Allow = &allowValues
	}
	if filterValue.Deny != nil {
		denyValues := cloneStrings(filterValue.Deny)
		wireValue.Deny = &denyValues
	}
	return wireValue, nil
}

func cloneDescriptor(source Descriptor) Descriptor {
	switch identity := source.(type) {
	case OneShotDescriptor:
		identity.Label = cloneString(identity.Label)
		return identity
	case ContinuableDescriptor:
		identity.AgentProvider = cloneString(identity.AgentProvider)
		identity.AgentModel = cloneString(identity.AgentModel)
		identity.Persona = cloneString(identity.Persona)
		filterValue, _ := cloneToolRestriction(identity.ToolFilter)
		identity.ToolFilter = filterValue
		return identity
	case BoundDescriptor:
		return identity
	default:
		return nil
	}
}

func cloneToolRestriction(
	filterValue *tools.ToolRestriction,
) (*tools.ToolRestriction, error) {
	if filterValue == nil {
		return nil, nil
	}
	if filterValue.Allow == nil && filterValue.Deny == nil {
		return nil, errors.New(
			"subagent: toolFilter must declare allow and/or deny",
		)
	}
	return &tools.ToolRestriction{
		Allow: cloneStrings(filterValue.Allow),
		Deny:  cloneStrings(filterValue.Deny),
	}, nil
}

func cloneStrings(source []string) []string {
	if source == nil {
		return nil
	}
	detached := make([]string, len(source))
	copy(detached, source)
	return detached
}

func cloneString(source *string) *string {
	if source == nil {
		return nil
	}
	detachedValue := *source
	return &detachedValue
}

// DescriptorEvent is the owner-defined typed Session event identity.
var DescriptorEvent = session.DefineEvent[DescriptorData](DescriptorEventName)

var _ json.Marshaler = DescriptorData{}
var _ json.Unmarshaler = (*DescriptorData)(nil)
