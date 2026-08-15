package llm

import (
	"encoding/json"
	"errors"

	"github.com/gorenx/goren/internal/jsonvalue"
)

// FinishReason is the merge-extensible terminal outcome contract.
type FinishReason interface {
	ReasonKind() string
	CloneReason() (FinishReason, error)
}

type StopFinish struct {
	Kind string `json:"kind"`
}

func (StopFinish) ReasonKind() string { return "stop" }
func (reason StopFinish) CloneReason() (FinishReason, error) {
	reason.Kind = "stop"
	return reason, nil
}

type ToolCallsFinish struct {
	Kind string `json:"kind"`
}

func (ToolCallsFinish) ReasonKind() string { return "tool-calls" }
func (reason ToolCallsFinish) CloneReason() (FinishReason, error) {
	reason.Kind = "tool-calls"
	return reason, nil
}

type MaxTokensFinish struct {
	Kind string `json:"kind"`
}

func (MaxTokensFinish) ReasonKind() string { return "max-tokens" }
func (reason MaxTokensFinish) CloneReason() (FinishReason, error) {
	reason.Kind = "max-tokens"
	return reason, nil
}

type AbortedFinish struct {
	Kind    string     `json:"kind"`
	Failure LlmFailure `json:"failure"`
}

func (AbortedFinish) ReasonKind() string { return "aborted" }
func (reason AbortedFinish) CloneReason() (FinishReason, error) {
	if err := validateFailure(reason.Failure); err != nil {
		return nil, err
	}
	reason.Kind = "aborted"
	reason.Failure = cloneFailure(reason.Failure)
	return reason, nil
}

type ErrorFinish struct {
	Kind    string     `json:"kind"`
	Failure LlmFailure `json:"failure"`
}

func (ErrorFinish) ReasonKind() string { return "error" }
func (reason ErrorFinish) CloneReason() (FinishReason, error) {
	if err := validateFailure(reason.Failure); err != nil {
		return nil, err
	}
	reason.Kind = "error"
	reason.Failure = cloneFailure(reason.Failure)
	return reason, nil
}

// OpaqueFinishReason preserves a provider/plugin extension reason.
type OpaqueFinishReason struct {
	kindName string
	rawValue json.RawMessage
}

func NewOpaqueFinishReason(kindName string, rawValue json.RawMessage) (OpaqueFinishReason, error) {
	if kindName == "" {
		return OpaqueFinishReason{}, errors.New("llm: opaque finish kind is empty")
	}
	detached, err := jsonvalue.Clone(rawValue)
	if err != nil || !jsonvalue.IsObject(detached) {
		return OpaqueFinishReason{}, errors.New("llm: opaque finish reason must be a lossless JSON object")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(detached, &fields); err != nil {
		return OpaqueFinishReason{}, err
	}
	var encodedKind string
	if err := json.Unmarshal(fields["kind"], &encodedKind); err != nil || encodedKind != kindName {
		return OpaqueFinishReason{}, errors.New("llm: opaque finish discriminant does not match")
	}
	return OpaqueFinishReason{kindName: kindName, rawValue: detached}, nil
}

func (reason OpaqueFinishReason) ReasonKind() string { return reason.kindName }
func (reason OpaqueFinishReason) CloneReason() (FinishReason, error) {
	return NewOpaqueFinishReason(reason.kindName, reason.rawValue)
}
func (reason OpaqueFinishReason) MarshalJSON() ([]byte, error) {
	if reason.kindName == "" || len(reason.rawValue) == 0 {
		return nil, errors.New("llm: invalid opaque finish reason")
	}
	return append([]byte(nil), reason.rawValue...), nil
}

// DecodeFinishReason restores core terminal outcomes and preserves extensions.
func DecodeFinishReason(rawValue json.RawMessage) (FinishReason, error) {
	if !jsonvalue.IsObject(rawValue) {
		return nil, errors.New("llm: finish reason must be an object")
	}
	var header struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(rawValue, &header); err != nil || header.Kind == "" {
		return nil, errors.New("llm: finish reason kind is missing")
	}
	switch header.Kind {
	case "stop":
		var reason StopFinish
		if err := decodeStrict(rawValue, &reason); err != nil {
			return nil, err
		}
		return reason.CloneReason()
	case "tool-calls":
		var reason ToolCallsFinish
		if err := decodeStrict(rawValue, &reason); err != nil {
			return nil, err
		}
		return reason.CloneReason()
	case "max-tokens":
		var reason MaxTokensFinish
		if err := decodeStrict(rawValue, &reason); err != nil {
			return nil, err
		}
		return reason.CloneReason()
	case "aborted":
		var reason AbortedFinish
		if err := decodeStrict(rawValue, &reason); err != nil {
			return nil, err
		}
		return reason.CloneReason()
	case "error":
		var reason ErrorFinish
		if err := decodeStrict(rawValue, &reason); err != nil {
			return nil, err
		}
		return reason.CloneReason()
	default:
		return NewOpaqueFinishReason(header.Kind, rawValue)
	}
}
