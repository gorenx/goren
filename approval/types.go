// Package approval owns user approval policy, durable audit events, and the
// scoped answerer seam used by permission-sensitive operations.
package approval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
)

const (
	// ServiceName is the canonical Cordis service name.
	ServiceName = "approval"
	// RequestEventName is the scoped approval answerer waterfall.
	RequestEventName = "approval/request"
	// AskedEventName is the durable audit event written before dispatch.
	AskedEventName = "approval/asked"
	// DecidedEventName is the durable audit event paired with AskedEventName.
	DecidedEventName = "approval/decided"
	// PolicyEventName records one durable per-session policy override.
	PolicyEventName = "approval/policy"
)

// RequestID pairs one asked audit event with exactly one decided event.
type RequestID string

// Outcome is the complete host-side approval result vocabulary.
type Outcome string

const (
	OutcomeAllowedOnce Outcome = "allowed-once"
	OutcomeRejected    Outcome = "rejected"
	OutcomeCancelled   Outcome = "cancelled"
	OutcomeUnavailable Outcome = "unavailable"
)

// Policy decides whether an approval request reaches interactive answerers.
type Policy string

const (
	PolicyAsk   Policy = "ask"
	PolicyNever Policy = "never"
)

// PolicySource identifies a durable policy override seeded by delegation.
type PolicySource string

const PolicySourceDelegation PolicySource = "delegation"

// Request is one readonly approval decision borrowed by answerers.
type Request struct {
	Subject  agent.Agent
	ToolName string
	CallID   *llm.CallID
	Reason   *string
}

// Asked is the durable pre-dispatch approval audit payload.
type Asked struct {
	ID       RequestID   `json:"id"`
	ToolName string      `json:"toolName"`
	CallID   *llm.CallID `json:"callId,omitempty"`
	Reason   *string     `json:"reason,omitempty"`
}

// Decided is the durable terminal approval audit payload.
type Decided struct {
	ID      RequestID `json:"id"`
	Outcome Outcome   `json:"outcome"`
}

// PolicyChanged is the durable per-session approval-policy override.
type PolicyChanged struct {
	Policy Policy        `json:"policy"`
	Source *PolicySource `json:"source,omitempty"`
}

var (
	AskedEvent   = session.DefineEvent[Asked](AskedEventName)
	DecidedEvent = session.DefineEvent[Decided](DecidedEventName)
	PolicyEvent  = session.DefineEvent[PolicyChanged](PolicyEventName)
)

// Config is the owner-defined typed Approval configuration.
type Config struct {
	Policy *Policy `json:"policy,omitempty"`
}

// ValidatedConfig is the immutable Approval configuration snapshot.
type ValidatedConfig struct {
	policy Policy
}

// ValidateConfig applies the source default and rejects unknown policies.
func ValidateConfig(settings Config) (ValidatedConfig, error) {
	selectedPolicy := PolicyAsk
	if settings.Policy != nil {
		selectedPolicy = *settings.Policy
	}
	if !validPolicy(selectedPolicy) {
		return ValidatedConfig{}, errors.New("approval: policy must be ask or never")
	}
	return ValidatedConfig{policy: selectedPolicy}, nil
}

// UnmarshalJSON preserves omission while rejecting null and unknown fields.
func (settings *Config) UnmarshalJSON(encoded []byte) error {
	if settings == nil {
		return errors.New("approval: cannot decode config into nil target")
	}
	if bytes.Equal(bytes.TrimSpace(encoded), []byte("null")) {
		return errors.New("approval: config must be an object")
	}
	var wire struct {
		Policy json.RawMessage `json:"policy"`
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return err
	}
	var decoded Config
	if len(wire.Policy) != 0 {
		if bytes.Equal(bytes.TrimSpace(wire.Policy), []byte("null")) {
			return errors.New("approval: policy must be ask or never")
		}
		var selectedPolicy Policy
		if err := json.Unmarshal(wire.Policy, &selectedPolicy); err != nil {
			return fmt.Errorf("approval: policy must be ask or never: %w", err)
		}
		decoded.Policy = &selectedPolicy
	}
	*settings = decoded
	return nil
}

// Approval is the provider-owned policy and decision capability.
type Approval interface {
	Request(context.Context, Request) (Outcome, error)
	EffectivePolicy(*session.Session) (Policy, error)
	OverrideOf(*session.Session) (Policy, bool, error)
	SetPolicy(context.Context, agent.Agent, Policy) error
}

// Service is the canonical Approval service definition.
var Service = plugin.DefineService[Approval](ServiceName)

func validOutcome(selectedOutcome Outcome) bool {
	switch selectedOutcome {
	case OutcomeAllowedOnce, OutcomeRejected, OutcomeCancelled, OutcomeUnavailable:
		return true
	default:
		return false
	}
}

func validPolicy(selectedPolicy Policy) bool {
	return selectedPolicy == PolicyAsk || selectedPolicy == PolicyNever
}

func cloneCallID(source *llm.CallID) *llm.CallID {
	if source == nil {
		return nil
	}
	snapshot := *source
	return &snapshot
}

func cloneString(source *string) *string {
	if source == nil {
		return nil
	}
	snapshot := *source
	return &snapshot
}
